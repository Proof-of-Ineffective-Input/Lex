package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// fxtwitterBase FxTwitter API 基址，变量以便测试替换。
var fxtwitterBase = "https://api.fxtwitter.com"

// xDirectFetcher 直接 fetch 兜底，变量以便测试替换。
var xDirectFetcher = func(ctx context.Context, client *http.Client, target string, limit int) (string, error) {
	return (HTMLHook{}).Fetch(ctx, client, target, limit)
}

// xURLKind 标识 X URL 的形态。
type xURLKind int

const (
	xStatusURL xURLKind = iota
	xProfileURL
	xSearchURL
)

// xURLInfo 解析后的 X URL 信息。
type xURLInfo struct {
	kind   xURLKind
	id     string
	handle string
	query  string
}

var (
	xStatusRe  = regexp.MustCompile(`https?://(?:x|twitter)\.com/[A-Za-z0-9_]+/status/(\d+)`)
	xSearchRe  = regexp.MustCompile(`https?://(?:x|twitter)\.com/search\?`)
	xProfileRe = regexp.MustCompile(`https?://(?:x|twitter)\.com/[A-Za-z0-9_]+`)
	handleRe   = regexp.MustCompile(`https?://(?:x|twitter)\.com/([A-Za-z0-9_]+)`)
)

// XHook 处理 Twitter/X URL（FxTwitter API 主链路 + 直接 fetch 兜底）。
type XHook struct{}

// Name 实现 Hook。
func (XHook) Name() string { return "x" }

// Match 实现 Hook：URL 命中 x.com|twitter.com 的 status/profile/search。
func (XHook) Match(target string) bool {
	info := classifyXURL(target)
	return info.id != "" || info.handle != "" || info.query != ""
}

// Fetch 实现 Hook：FxTwitter API → 直接 fetch 降级链。
func (XHook) Fetch(ctx context.Context, client *http.Client, target string, limit int) (string, error) {
	info := classifyXURL(target)
	if info.id == "" && info.handle == "" && info.query == "" {
		return xDirectFetcher(ctx, client, target, limit)
	}
	out, err := xFetchFxtwitter(ctx, client, info)
	if err != nil {
		return xDirectFetcher(ctx, client, target, limit)
	}
	return Truncate(out, limit), nil
}

// classifyXURL 解析 X URL，返回形态与关键参数。
func classifyXURL(target string) xURLInfo {
	if m := xStatusRe.FindStringSubmatch(target); len(m) >= 2 {
		return xURLInfo{kind: xStatusURL, id: m[1], handle: extractHandle(target)}
	}
	if xSearchRe.MatchString(target) {
		return xURLInfo{kind: xSearchURL, query: extractSearchQuery(target)}
	}
	if xProfileRe.MatchString(target) {
		return xURLInfo{kind: xProfileURL, handle: extractHandle(target)}
	}
	return xURLInfo{}
}

func extractHandle(target string) string {
	m := handleRe.FindStringSubmatch(target)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

func extractSearchQuery(target string) string {
	u, err := url.Parse(target)
	if err != nil {
		return ""
	}
	return u.Query().Get("q")
}

// xVerification 用户认证状态。
type xVerification struct {
	Verified bool `json:"verified"`
}

// xAPIUser 用户信息。
type xAPIUser struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	ScreenName   string        `json:"screen_name"`
	Description  string        `json:"description"`
	Followers    int           `json:"followers"`
	Following    int           `json:"following"`
	Statuses     int           `json:"statuses"`
	Verification xVerification `json:"verification"`
}

// xStatus 推文信息。
type xStatus struct {
	ID          string   `json:"id"`
	URL         string   `json:"url"`
	Text        string   `json:"text"`
	CreatedAt   string   `json:"created_at"`
	CreatedTs   int64    `json:"created_timestamp"`
	Likes       int      `json:"likes"`
	Reposts     int      `json:"reposts"`
	Quotes      int      `json:"quotes"`
	Replies     int      `json:"replies"`
	Views       *int64   `json:"views"`
	Bookmarks   int      `json:"bookmarks"`
	Author      xAPIUser `json:"author"`
	Lang        string   `json:"lang"`
	Source      string   `json:"source"`
	IsNoteTweet bool     `json:"is_note_tweet"`
}

// xCursor 分页游标。
type xCursor struct {
	Top    string `json:"top"`
	Bottom string `json:"bottom"`
}

// xConversation 评论区响应（status + replies）。
type xConversation struct {
	Status  xStatus   `json:"status"`
	Replies []xStatus `json:"replies"`
	Author  xAPIUser  `json:"author"`
	Cursor  xCursor   `json:"cursor"`
	Code    int       `json:"code"`
}

// xSearchResults 搜索/时间线响应（results 列表）。
type xSearchResults struct {
	Code    int       `json:"code"`
	Results []xStatus `json:"results"`
	Cursor  xCursor   `json:"cursor"`
}

// xFetchFxtwitter 按 URL 形态调用 FxTwitter API 并组装 Markdown。
func xFetchFxtwitter(ctx context.Context, client *http.Client, info xURLInfo) (string, error) {
	endpoint := ""
	switch info.kind {
	case xStatusURL:
		endpoint = fmt.Sprintf("/2/conversation/%s", info.id)
	case xProfileURL:
		endpoint = fmt.Sprintf("/2/profile/%s/statuses", info.handle)
	case xSearchURL:
		endpoint = "/2/search"
	}
	u := fxtwitterBase + endpoint
	if info.kind == xSearchURL {
		u += "?q=" + url.QueryEscape(info.query)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", UA)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var codeWrap struct {
		Code int `json:"code"`
	}
	_ = json.Unmarshal(body, &codeWrap)

	// 404 双义：search / profile 的 404 可能是空结果而非错误，视为空结果返回。
	emptyOK := info.kind == xSearchURL || info.kind == xProfileURL
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound && emptyOK {
			return "", nil
		}
		return "", fmt.Errorf("fxtwitter status %d", resp.StatusCode)
	}
	if codeWrap.Code != 200 {
		if codeWrap.Code == http.StatusNotFound && emptyOK {
			return "", nil
		}
		return "", fmt.Errorf("fxtwitter code %d", codeWrap.Code)
	}

	switch info.kind {
	case xStatusURL:
		var conv xConversation
		if err := json.Unmarshal(body, &conv); err != nil {
			return "", err
		}
		return formatConversation(conv), nil
	default:
		var sr xSearchResults
		if err := json.Unmarshal(body, &sr); err != nil {
			return "", err
		}
		return formatSearchResults(sr), nil
	}
}

func formatConversation(conv xConversation) string {
	var sb strings.Builder
	sb.WriteString(formatStatus(conv.Status, conv.Author))
	if len(conv.Replies) > 0 {
		sb.WriteString("\n\nReplies:\n")
		for i, r := range conv.Replies {
			if i > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(formatStatus(r, r.Author))
		}
	}
	// 有 bottom 游标说明可能还有更多回复，标记 partial。
	if conv.Cursor.Bottom != "" {
		sb.WriteString("\n\n(partial: more replies available via cursor)")
	}
	return sb.String()
}

func formatSearchResults(sr xSearchResults) string {
	var sb strings.Builder
	for i, r := range sr.Results {
		if i > 0 {
			sb.WriteString("\n\n---\n\n")
		}
		sb.WriteString(formatStatus(r, r.Author))
	}
	return sb.String()
}

func formatStatus(s xStatus, author xAPIUser) string {
	var sb strings.Builder
	if author.ScreenName != "" {
		sb.WriteString(fmt.Sprintf("@%s (%s)\n", author.ScreenName, author.Name))
	}
	sb.WriteString(s.Text)
	sb.WriteString("\n")
	var meta []string
	meta = append(meta, fmt.Sprintf("likes: %d", s.Likes))
	meta = append(meta, fmt.Sprintf("reposts: %d", s.Reposts))
	meta = append(meta, fmt.Sprintf("replies: %d", s.Replies))
	if s.Views != nil {
		meta = append(meta, fmt.Sprintf("views: %d", *s.Views))
	}
	if s.CreatedAt != "" {
		meta = append(meta, s.CreatedAt)
	}
	sb.WriteString(strings.Join(meta, " | "))
	return sb.String()
}

func init() {
	Register(XHook{})
}
