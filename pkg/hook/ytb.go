package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"lex/pkg/rerank"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var videoIDRe = regexp.MustCompile(`(?:youtube\.com/(?:watch\?v=|shorts/|embed/|live/|v/)|youtu\.be/)([A-Za-z0-9_-]{11})`)

type oembed struct {
	Title      string `json:"title"`
	AuthorName string `json:"author_name"`
	AuthorURL  string `json:"author_url"`
}

type videoDetails struct {
	LengthSeconds    string `json:"lengthSeconds"`
	ViewCount        string `json:"viewCount"`
	PublishDate      string `json:"publishDate"`
	ShortDescription string `json:"shortDescription"`
}

type ytInitialPlayerResponse struct {
	VideoDetails videoDetails `json:"videoDetails"`
}

type ytdlpInfo struct {
	Uploader    string         `json:"uploader"`
	Duration    float64        `json:"duration"`
	ViewCount   float64        `json:"view_count"`
	LikeCount   float64        `json:"like_count"`
	UploadDate  string         `json:"upload_date"`
	Description string         `json:"description"`
	Comments    []ytdlpComment `json:"comments"`
}

type ytdlpComment struct {
	Author    string  `json:"author"`
	Text      string  `json:"text"`
	LikeCount float64 `json:"like_count"`
	IsPinned  bool    `json:"is_pinned"`
}

var ytInitialPlayerResponseRe = regexp.MustCompile(`ytInitialPlayerResponse\s*=\s*(\{.*?\});`)

// ExaFetcher 外部注入的 Exa 抓取回调，用于字幕缺失时的隐式兜底
var ExaFetcher func(ctx context.Context, client *http.Client, target string, limit int) (string, error)

// YTHook 处理 YouTube 视频 URL（oEmbed + yt-dlp 字幕/评论）。
type YTHook struct{}

// Name 实现 Hook。
func (YTHook) Name() string { return "youtube" }

// Match 实现 Hook：URL 含 YouTube 视频 ID。
func (YTHook) Match(target string) bool {
	return videoIDRe.MatchString(target)
}

// Fetch 实现 Hook。
func (YTHook) Fetch(ctx context.Context, client *http.Client, target string, limit int) (string, error) {
	id := ExtractVideoID(target)
	if id == "" {
		return "", fmt.Errorf("not a valid YouTube URL: %s", target)
	}

	var sb strings.Builder

	meta, err := fetchOEmbed(ctx, client, id)
	if err != nil {
		return "", err
	}
	sb.WriteString(fmt.Sprintf("Title: %s\n", meta.Title))
	sb.WriteString(fmt.Sprintf("Author: %s\n", meta.AuthorName))
	if meta.AuthorURL != "" {
		sb.WriteString(fmt.Sprintf("Channel URL: %s\n", meta.AuthorURL))
	}

	enhanced, _ := fetchEnhanced(ctx, client, target)
	if enhanced != nil {
		if enhanced.LengthSeconds != "" {
			sb.WriteString(fmt.Sprintf("Duration: %ss\n", enhanced.LengthSeconds))
		}
		if enhanced.ViewCount != "" {
			sb.WriteString(fmt.Sprintf("Views: %s\n", enhanced.ViewCount))
		}
		if enhanced.PublishDate != "" {
			sb.WriteString(fmt.Sprintf("Published: %s\n", enhanced.PublishDate))
		}
	}
	sb.WriteString(fmt.Sprintf("URL: %s\n", target))

	transcript, comments, description, ytErr := ytdlpFetch(ctx, id)
	if transcript == "" && ExaFetcher != nil {
		// yt-dlp 未获取到字幕，尝试 Exa MCP 自动兜底
		if exaText, err := ExaFetcher(ctx, client, target, limit); err == nil && strings.TrimSpace(exaText) != "" {
			transcript = exaText
		}
	}

	if ytErr != nil && transcript == "" && len(comments) == 0 {
		sb.WriteString("\n\n---\n")
		sb.WriteString(ytErr.Error())
		return sb.String(), nil
	}

	discText := ""
	if len(comments) > 0 {
		discText = formatComments(comments)
	}

	tLen := utf8.RuneCountInString(transcript)
	dLen := utf8.RuneCountInString(discText)

	transcriptBudget, discussionBudget := splitBudget(limit, tLen, dLen)

	if transcript == "" && len(comments) > 0 {
		sb.WriteString("\n\nNote: No transcript available for this video; full budget allocated to discussion.")
	} else if transcript != "" && len(comments) == 0 {
		sb.WriteString("\n\nNote: No comments available for this video; full budget allocated to transcript.")
	}

	if transcript != "" {
		sb.WriteString("\n\nTranscript:\n")
		query := strings.TrimSpace(meta.Title + " " + description)
		sb.WriteString(rerank.RerankByChars(transcript, query, transcriptBudget))
	}
	if len(comments) > 0 {
		sb.WriteString("\n\nDiscussion:\n")
		sb.WriteString(Truncate(discText, discussionBudget))
	}

	return sb.String(), nil
}

func splitBudget(limit, tLen, dLen int) (transcript, discussion int) {
	if limit <= 0 {
		return 0, 0
	}
	if tLen <= 0 && dLen <= 0 {
		return 0, 0
	}
	if tLen <= 0 {
		return 0, limit
	}
	if dLen <= 0 {
		return limit, 0
	}

	pct := math.Round(float64(tLen) / float64(tLen+dLen) * 100.0)
	tBudget := int(math.Round(float64(limit) * pct / 100.0))
	dBudget := limit - tBudget
	return tBudget, dBudget
}

func formatComments(comments []ytdlpComment) string {
	var b strings.Builder
	for i, c := range comments {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(fmt.Sprintf("- %s: %s (likes: %d)", c.Author, c.Text, int(c.LikeCount)))
	}
	return b.String()
}

func ExtractVideoID(target string) string {
	m := videoIDRe.FindStringSubmatch(target)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func init() {
	Register(YTHook{})
}

func fetchOEmbed(ctx context.Context, client *http.Client, id string) (*oembed, error) {
	u := fmt.Sprintf("https://www.youtube.com/oembed?format=json&url=%s",
		url.QueryEscape("https://www.youtube.com/watch?v="+id))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UA)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oEmbed returned status %d", resp.StatusCode)
	}
	var m oembed
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

func fetchEnhanced(ctx context.Context, client *http.Client, target string) (*videoDetails, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UA)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	m := ytInitialPlayerResponseRe.FindSubmatch(body)
	if len(m) < 2 {
		return nil, fmt.Errorf("ytInitialPlayerResponse not found on watch page")
	}
	var pr ytInitialPlayerResponse
	if err := json.Unmarshal(m[1], &pr); err != nil {
		return nil, err
	}
	return &pr.VideoDetails, nil
}

type dlErr struct {
	msg string
}

func (e *dlErr) Error() string { return e.msg }

const dlErrNoYtDlp = `[ytb] yt-dlp not found; unable to fetch subtitles and comments, returning metadata only.
Please install and configure yt-dlp to enable full functionality:
  - Install: https://github.com/yt-dlp/yt-dlp/releases (or pip install yt-dlp)
  - For member/age-restricted videos, configure browser cookies (yt-dlp --cookies-from-browser <browser>)`

const dlErrNoSubs = "[yt-dlp] Failed to fetch subtitles and comments (no subtitles, login required, or rate-limited); returning metadata only."

// dlErrPoToken 指示 YouTube 要求 PO Token 且 web_embedded 也未返回字幕。
const dlErrPoToken = "[yt-dlp] YouTube requires a PO Token (Proof of Origin Token) to serve subtitles for this video, and the web_embedded client also returned no subtitles.\nRecommended: upgrade yt-dlp to the latest version, or install a PO Token Provider plugin (https://github.com/yt-dlp/yt-dlp/wiki/PO-Token-Guide)."

var supportedBrowsers = []string{
	"edge", "chrome", "chromium", "firefox", "brave", "opera", "vivaldi", "whale", "safari",
}

var statePath = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".lex", "ytb-browser.txt")
}

func isSupportedBrowser(name string) bool {
	for _, b := range supportedBrowsers {
		if name == b {
			return true
		}
	}
	return false
}

func saveLastBrowser(name string) {
	p := statePath()
	if p == "" || !isSupportedBrowser(name) {
		return
	}
	_ = os.WriteFile(p, []byte(name), 0o600)
}

func loadLastBrowser() string {
	p := statePath()
	if p == "" {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(data))
	if !isSupportedBrowser(name) {
		return ""
	}
	return name
}

func browserRetryOrder() []string {
	var order []string
	add := func(name string) {
		if name == "" || !isSupportedBrowser(name) {
			return
		}
		for _, b := range order {
			if b == name {
				return
			}
		}
		order = append(order, name)
	}
	if b := os.Getenv("LEX_YT_BROWSER"); b != "" {
		add(b)
	}
	add(loadLastBrowser())
	for _, b := range supportedBrowsers {
		add(b)
	}
	order = append(order, "")
	return order
}

func newDlErr(stderr string) *dlErr {
	msg := "[yt-dlp] Failed to fetch subtitles/comments; returning metadata only."
	if s := strings.TrimSpace(stderr); s != "" {
		if len(s) > 300 {
			s = s[:300]
		}
		msg += "\nstderr: " + s
	}
	return &dlErr{msg: msg}
}

// cookieMode 描述一次 yt-dlp 调用的 cookie 策略：
// browser 非空则加 --cookies-from-browser，cacheFile 非空则加 --cookies。
// 两者同时非空时，yt-dlp 会从浏览器提取 cookies 并写入 cacheFile（一边抓取一边更新缓存）。
type cookieMode struct {
	browser   string
	cacheFile string
}

func (c cookieMode) args() []string {
	var a []string
	if c.browser != "" {
		a = append(a, "--cookies-from-browser", c.browser)
	}
	if c.cacheFile != "" {
		a = append(a, "--cookies", c.cacheFile)
	}
	return a
}

// defaultCookiesPath 返回静态 cookie 缓存路径：{userprofile}/.lex/cookies.txt。
// 读取失败返回 ""。作为变量以便测试替换。
var defaultCookiesPath = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".lex", "cookies.txt")
}

func ytdlpFetch(ctx context.Context, id string) (string, []ytdlpComment, string, error) {
	path, err := exec.LookPath("yt-dlp")
	if err != nil {
		return "", nil, "", &dlErr{msg: dlErrNoYtDlp}
	}

	tmp, err := os.MkdirTemp("", "lex-ytb-*")
	if err != nil {
		return "", nil, "", newDlErr(err.Error())
	}
	defer os.RemoveAll(tmp)

	// 第零层：web_embedded 无 cookie 快路径（字幕无需 PO Token，不依赖 cookies）。
	poTokenMiss := false
	o := runYtdlp(ctx, path, tmp, id, cookieMode{})
	if o.subOK || o.commentOK {
		return o.transcript, o.comments, o.description, nil
	}
	poTokenMiss = o.poTokenMiss

	var tried []string
	// 第一层：单参数 --cookies-from-browser 直抓所有浏览器。
	// 失败的浏览器秒报错（浏览器锁定/无该浏览器），延迟可忽略，可并行。
	for _, b := range browserRetryOrder() {
		if b == "" {
			continue
		}
		tried = append(tried, b)
		o := runYtdlp(ctx, path, tmp, id, cookieMode{browser: b})
		if o.subOK || o.commentOK {
			saveLastBrowser(b)
			return o.transcript, o.comments, o.description, nil
		}
		poTokenMiss = poTokenMiss || o.poTokenMiss
	}

	// 第二层：直抓全失败，探测 dump 刷新缓存文件，再用缓存抓取。
	cache := defaultCookiesPath()
	for _, b := range browserRetryOrder() {
		if b == "" {
			continue
		}
		if !probeCookies(ctx, path, b, cache) {
			continue
		}
		o := runYtdlp(ctx, path, tmp, id, cookieMode{cacheFile: cache})
		if o.subOK || o.commentOK {
			saveLastBrowser(b)
			return o.transcript, o.comments, o.description, nil
		}
		poTokenMiss = poTokenMiss || o.poTokenMiss
	}

	// 第三层：静态缓存兜底（浏览器探测全部失败，如浏览器锁定）。
	if cache != "" {
		if _, err := os.Stat(cache); err == nil {
			o := runYtdlp(ctx, path, tmp, id, cookieMode{cacheFile: cache})
			if o.subOK || o.commentOK {
				return o.transcript, o.comments, o.description, nil
			}
			poTokenMiss = poTokenMiss || o.poTokenMiss
		}
	}

	// 原第四层（无 cookie 兜底）已由第零层吸收。

	if poTokenMiss {
		return "", nil, "", &dlErr{msg: dlErrPoToken}
	}
	return "", nil, "", &dlErr{msg: dlErrNoSubs + "\nTried browser cookies " + strings.Join(tried, "/") + ", cached cookies, and no cookies; all failed (no subtitles, login required, or rate-limited).\nSet LEX_YT_BROWSER to specify a browser, e.g. LEX_YT_BROWSER=edge."}
}

// probeCookies 探测 yt-dlp 能否从浏览器导出 cookies 并写入缓存文件。
// 用一个不存在的占位符视频跳过一切下载，只触发 cookie 导出逻辑。
// 关键：yt-dlp 导出 cookie 成功时会向 stderr 打印 "Extracted N cookies from <browser>"，
// 即使后续因占位符视频报 "Video unavailable"（退出码非 0），导出仍已成功。
// 因此不能只看退出码，必须解析 stderr。
func probeCookies(ctx context.Context, path, browser, cacheFile string) bool {
	if cacheFile == "" {
		return false
	}
	args := []string{
		"--cookies-from-browser", browser,
		"--cookies", cacheFile,
		"--skip-download",
		"--no-progress", "--no-warnings",
		"https://www.youtube.com/watch?v=__lex_cookie_probe__",
	}
	cmd := exec.CommandContext(ctx, path, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard
	_ = cmd.Run()
	s := stderr.String()
	// 解析 "Extracted N cookies from <browser>" 确认导出成功。
	probeRe := regexp.MustCompile(`Extracted\s+\d+\s+cookies?\s+from\s+` + regexp.QuoteMeta(browser))
	return probeRe.MatchString(s)
}

// ytdlpOutcome 汇总一次 yt-dlp 调用的字幕/评论结果与诊断信息。
type ytdlpOutcome struct {
	transcript  string
	comments    []ytdlpComment
	description string
	subOK       bool
	commentOK   bool
	poTokenMiss bool
	stderr      string
}

// hasPoTokenMiss 检测 yt-dlp stderr 中是否出现 PO Token 缺失的 WARNING。
func hasPoTokenMiss(stderr string) bool {
	return strings.Contains(strings.ToLower(stderr), "po token")
}

// runYtdlp 用一次 yt-dlp 调用同时抓取字幕、评论与描述。
// 用 --write-info-json（非 simulate）替代 --dump-single-json（simulate），
// 使评论/描述写入 .info.json 文件的同时字幕也能落盘，避免功能冲突。
// 固定使用 web_embedded 客户端：字幕无需 PO Token，且支持 cookies。
// 不传 --no-warnings，以保留 PO Token 缺失的 WARNING 供诊断。
func runYtdlp(ctx context.Context, path, tmp, id string, m cookieMode) ytdlpOutcome {
	out := filepath.Join(tmp, "%(id)s.%(ext)s")
	args := []string{
		"--skip-download",
		"--write-info-json",
		"--write-comments",
		"--write-auto-sub",
		"--sub-lang", "en",
		"--convert-subs", "srt",
		"--output", out,
		"--no-progress",
		"--extractor-args", "youtube:player_client=web_embedded",
	}
	args = append(args, m.args()...)
	args = append(args, "https://www.youtube.com/watch?v="+id)
	cmd := exec.CommandContext(ctx, path, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard
	_ = cmd.Run()

	transcript, subOK := readTranscript(tmp)
	comments, description, commentOK := readComments(tmp, id)
	return ytdlpOutcome{
		transcript:  transcript,
		comments:    comments,
		description: description,
		subOK:       subOK,
		commentOK:   commentOK,
		poTokenMiss: hasPoTokenMiss(stderr.String()),
		stderr:      stderr.String(),
	}
}

// readTranscript 从 tmp 目录读取转换后的 srt 字幕。
func readTranscript(tmp string) (string, bool) {
	files, _ := filepath.Glob(filepath.Join(tmp, "*.srt"))
	if len(files) == 0 {
		return "", false
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		return "", false
	}
	transcript := parseSRT(string(data))
	if strings.TrimSpace(transcript) == "" {
		return "", false
	}
	return transcript, true
}

// readComments 从 tmp 目录读取 %(id)s.info.json 中的评论与描述。
func readComments(tmp, id string) ([]ytdlpComment, string, bool) {
	data, err := os.ReadFile(filepath.Join(tmp, id+".info.json"))
	if err != nil {
		return nil, "", false
	}
	var info ytdlpInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, "", false
	}
	if len(info.Comments) == 0 {
		return nil, "", false
	}
	return info.Comments, info.Description, true
}

func parseSRT(content string) string {
	tagRe := regexp.MustCompile(`<[^>]+>`)
	var lines []string
	prev := ""
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(tagRe.ReplaceAllString(raw, ""))
		if line == "" || isNumeric(line) || strings.Contains(line, "-->") || strings.HasPrefix(line, "WEBVTT") {
			continue
		}
		if line != prev {
			lines = append(lines, line)
		}
		prev = line
	}
	return strings.Join(lines, "\n")
}

func isNumeric(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func Truncate(content string, limit int) string {
	if limit <= 0 || len(content) <= limit {
		return content
	}
	return fmt.Sprintf("%s\n\n---\n*Content truncated to %d characters (original: %d characters)*",
		content[:limit], limit, len(content))
}

func formatDuration(sec float64) string {
	d := time.Duration(sec) * time.Second
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
