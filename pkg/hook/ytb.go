// Package hook 提供 FetchSingle 的旁路钩子。
// ytb.go 是 YouTube 旁路：oEmbed 拿基础元数据（零依赖、极稳），
// yt-dlp 拿字幕与评论（失败降级只返回元数据 + 给调用方 AI 的提示）。
package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mcp-search-duckduckgo/pkg/rerank"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// videoIDRe 匹配各种 YouTube URL 形式并提取 11 位视频 ID。
var videoIDRe = regexp.MustCompile(`(?:youtube\.com/(?:watch\?v=|shorts/|embed/|live/|v/)|youtu\.be/)([A-Za-z0-9_-]{11})`)

// oembed 是 YouTube oEmbed 端点的响应结构。
type oembed struct {
	Title      string `json:"title"`
	AuthorName string `json:"author_name"`
	AuthorURL  string `json:"author_url"`
}

// videoDetails 是 watch 页 ytInitialPlayerResponse 中 videoDetails 的字段。
type videoDetails struct {
	LengthSeconds    string `json:"lengthSeconds"`
	ViewCount        string `json:"viewCount"`
	PublishDate      string `json:"publishDate"`
	ShortDescription string `json:"shortDescription"`
}

// ytInitialPlayerResponse 是 watch 页内嵌 JSON 的顶层结构。
type ytInitialPlayerResponse struct {
	VideoDetails videoDetails `json:"videoDetails"`
}

// ytdlpInfo 是 yt-dlp --dump-single-json 输出的关键字段。
type ytdlpInfo struct {
	Uploader    string         `json:"uploader"`
	Duration    float64        `json:"duration"`
	ViewCount   float64        `json:"view_count"`
	LikeCount   float64        `json:"like_count"`
	UploadDate  string         `json:"upload_date"`
	Description string         `json:"description"`
	Comments    []ytdlpComment `json:"comments"`
}

// ytdlpComment 是 yt-dlp 评论条目。
type ytdlpComment struct {
	Author    string  `json:"author"`
	Text      string  `json:"text"`
	LikeCount float64 `json:"like_count"`
	IsPinned  bool    `json:"is_pinned"`
}

// ytInitialPlayerResponseRe 匹配 watch 页内嵌的 ytInitialPlayerResponse JSON。
var ytInitialPlayerResponseRe = regexp.MustCompile(`ytInitialPlayerResponse\s*=\s*(\{.*?\});`)

// Fetch 抓取一个 YouTube 视频，返回类 exa 的 plain text。
// 任何一层失败都降级，不整体报错；仅当连 oEmbed 都失败时才返回错误。
func Fetch(ctx context.Context, client *http.Client, target string, limit int) (string, error) {
	id := ExtractVideoID(target)
	if id == "" {
		return "", fmt.Errorf("not a valid YouTube URL: %s", target)
	}

	var sb strings.Builder

	// L0: oEmbed 元数据（必失败则整体失败）
	meta, err := fetchOEmbed(ctx, client, id)
	if err != nil {
		return "", err
	}
	sb.WriteString(fmt.Sprintf("Title: %s\n", meta.Title))
	sb.WriteString(fmt.Sprintf("Author: %s\n", meta.AuthorName))
	if meta.AuthorURL != "" {
		sb.WriteString(fmt.Sprintf("Channel URL: %s\n", meta.AuthorURL))
	}

	// L1: 增强元数据（duration/views/published），失败不影响
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

	// L2+L3: yt-dlp 拿字幕与评论（两条独立命令，避免 --dump-single-json 吞掉字幕文件）
	transcript, comments, description, ytErr := ytdlpFetch(ctx, id)
	if ytErr != nil {
		// 无 yt-dlp 或抓取失败：返回元数据 + 写给调用方 AI 的提示
		sb.WriteString("\n\n---\n")
		sb.WriteString(ytErr.Error())
		return sb.String(), nil
	}

	// limit 分配：元数据不计入；transcript 拿 2/3，discussion 拿 1/3。
	// limit<=0 表示不截断。
	transcriptBudget, discussionBudget := splitBudget(limit)

	if transcript != "" {
		sb.WriteString("\n\nTranscript:\n")
		// 用标题+描述当 query 对 transcript 做 BM25 rerank，过滤开头结尾客套话/广告。
		query := strings.TrimSpace(meta.Title + " " + description)
		sb.WriteString(rerank.RerankByChars(transcript, query, transcriptBudget))
	}
	if len(comments) > 0 {
		sb.WriteString("\n\nDiscussion:\n")
		// discussion 不 rerank，直接截断，避免偏见放大效应。
		sb.WriteString(Truncate(formatComments(comments), discussionBudget))
	}

	return sb.String(), nil
}

// splitBudget 把 limit 拆成 transcript 2/3 与 discussion 1/3。
// limit<=0 返回 (0,0)，表示不截断。
func splitBudget(limit int) (transcript, discussion int) {
	if limit <= 0 {
		return 0, 0
	}
	return limit * 2 / 3, limit / 3
}

// formatComments 把评论列表格式化为多行文本。
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

// ExtractVideoID 从任意 YouTube URL 提取 11 位视频 ID，失败返回空串。
func ExtractVideoID(target string) string {
	m := videoIDRe.FindStringSubmatch(target)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// fetchOEmbed 调用 YouTube oEmbed 端点拿基础元数据。
func fetchOEmbed(ctx context.Context, client *http.Client, id string) (*oembed, error) {
	u := fmt.Sprintf("https://www.youtube.com/oembed?format=json&url=%s",
		url.QueryEscape("https://www.youtube.com/watch?v="+id))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oEmbed 返回状态 %d", resp.StatusCode)
	}
	var m oembed
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

// fetchEnhanced 抓 watch 页并提取 ytInitialPlayerResponse 中的增强元数据。
func fetchEnhanced(ctx context.Context, client *http.Client, target string) (*videoDetails, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36")
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
		return nil, fmt.Errorf("watch 页未找到 ytInitialPlayerResponse")
	}
	var pr ytInitialPlayerResponse
	if err := json.Unmarshal(m[1], &pr); err != nil {
		return nil, err
	}
	return &pr.VideoDetails, nil
}

// dlErr 表示 yt-dlp 层失败，携带给调用方 AI 的提示。
type dlErr struct {
	msg string
}

func (e *dlErr) Error() string { return e.msg }

// dlErrNoYtDlp 是 yt-dlp 未安装时的提示，写给调用方 AI（而非用户）。
const dlErrNoYtDlp = `[ytb] 检测到系统未安装 yt-dlp，无法获取字幕与评论，仅返回元数据。
请通知用户安装并配置 yt-dlp 以启用完整功能：
  - 安装：https://github.com/yt-dlp/yt-dlp/releases （或 pip install yt-dlp）
  - 若需访问会员/年龄限制视频，请配置 cookies（yt-dlp --cookies cookies.txt）`

// dlErrNoSubs 表示字幕与评论均不可用（如无字幕、需登录、风控）。
const dlErrNoSubs = "[yt-dlp] 未能获取字幕与评论（可能无字幕、需登录或触发风控），仅返回元数据。"

// defaultCookiesPath 返回通用回退 cookies 路径：{userprofile}/.lex/cookies.txt。
// 读取失败返回 ""，调用方据此时决定是否省略 --cookies。
func defaultCookiesPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".lex", "cookies.txt")
}

// cookiesPath 解析 yt-dlp 的 cookies 文件路径：
// 1. 环境变量 LEX_YT_COOKIES 优先（显式指定）
// 2. 回退到 {userprofile}/.lex/cookies.txt
// 返回 "" 表示无可用 cookies。
func cookiesPath() string {
	if p := os.Getenv("LEX_YT_COOKIES"); p != "" {
		return p
	}
	return defaultCookiesPath()
}

// newDlErr 构造 yt-dlp 执行失败的错误，附带 stderr 摘要。
func newDlErr(stderr string) *dlErr {
	msg := "[yt-dlp] 抓取字幕/评论失败，仅返回元数据。"
	if s := strings.TrimSpace(stderr); s != "" {
		if len(s) > 300 {
			s = s[:300]
		}
		msg += "\nstderr: " + s
	}
	return &dlErr{msg: msg}
}

// ytdlpFetch 调用 yt-dlp 拿字幕与评论（两条独立命令）。
// 返回 (transcript, comments, description, error)。error 为 nil 表示成功；否则为 dlErr 类型。
// 字幕与评论任一成功即可，全部失败才报错。
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

	// cookies 绝对优先：先带 cookies 跑，失败再降级为无 cookies 重试。
	cookieFile := cookiesPath()
	retry := []string{cookieFile}
	if cookieFile != "" {
		retry = append(retry, "")
	}
	for _, cf := range retry {
		transcript, comments, description, ok := runYtdlp(ctx, path, tmp, id, cf)
		if ok {
			return transcript, comments, description, nil
		}
	}

	// 全部失败：报告并提示调用方配置 yt-dlp 与 cookies
	if cookieFile != "" {
		return "", nil, "", &dlErr{msg: dlErrNoSubs + "\ncookies 已配置于 " + cookieFile + " 仍失败，请检查 cookies 是否过期或需更新（yt-dlp --cookies " + cookieFile + "）"}
	}
	return "", nil, "", &dlErr{msg: dlErrNoSubs + "\n未找到 cookies。请配置 yt-dlp cookies 后重试：\n  - 设置环境变量 LEX_YT_COOKIES 指向 cookies.txt，或\n  - 将 cookies.txt 置于 {userprofile}/.lex/cookies.txt\n  - 参考：https://github.com/yt-dlp/yt-dlp/wiki/FAQ#how-do-i-pass-cookies-to-yt-dlp"}
}

// runYtdlp 执行 yt-dlp 抓取：先跑字幕命令，再跑评论+描述命令（dump-single-json）。
// 两条命令独立，避免 --dump-single-json 吞掉字幕文件。
// cookieFile 为空则不加 --cookies。ok=false 表示全部失败。
func runYtdlp(ctx context.Context, path, tmp, id, cookieFile string) (string, []ytdlpComment, string, bool) {
	transcript, subOK := runSubs(ctx, path, tmp, id, cookieFile)
	comments, description, commentOK := runComments(ctx, path, id, cookieFile)
	if !subOK && !commentOK {
		return "", nil, "", false
	}
	return transcript, comments, description, true
}

// runSubs 执行 yt-dlp 下载自动字幕（srt）。
func runSubs(ctx context.Context, path, tmp, id, cookieFile string) (string, bool) {
	out := filepath.Join(tmp, "%(id)s.%(ext)s")
	args := []string{
		"--skip-download",
		"--write-auto-sub",
		"--sub-lang", "en",
		"--convert-subs", "srt",
		"--output", out,
		"--no-progress", "--no-warnings",
	}
	if cookieFile != "" {
		args = append(args, "--cookies", cookieFile)
	}
	args = append(args, "https://www.youtube.com/watch?v="+id)
	cmd := exec.CommandContext(ctx, path, args...)
	if err := cmd.Run(); err != nil {
		return "", false
	}

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

// runComments 执行 yt-dlp --dump-single-json 拿评论与描述。
func runComments(ctx context.Context, path, id, cookieFile string) ([]ytdlpComment, string, bool) {
	args := []string{
		"--skip-download",
		"--write-comments",
		"--dump-single-json",
		"--no-progress", "--no-warnings",
	}
	if cookieFile != "" {
		args = append(args, "--cookies", cookieFile)
	}
	args = append(args, "https://www.youtube.com/watch?v="+id)
	cmd := exec.CommandContext(ctx, path, args...)
	stdout, err := cmd.Output()
	if err != nil {
		return nil, "", false
	}

	var info ytdlpInfo
	if err := json.Unmarshal(stdout, &info); err != nil {
		return nil, "", false
	}
	if len(info.Comments) == 0 {
		return nil, "", false
	}
	return info.Comments, info.Description, true
}

// parseSRT 清洗 SRT 字幕：去 HTML 标签、时间戳、序号、WEBVTT 头、相邻重复行。
// 每行字幕用换行分隔，保留成独立句子，供 rerank 按语义挑选后按时间戳顺序重排。
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

// isNumeric 判断字符串是否纯数字（SRT 序号行）。
func isNumeric(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// Truncate 按 limit 截断，limit<=0 表示不截断。
func Truncate(content string, limit int) string {
	if limit <= 0 || len(content) <= limit {
		return content
	}
	return fmt.Sprintf("%s\n\n---\n*Content truncated to %d characters (original: %d characters)*",
		content[:limit], limit, len(content))
}

// formatDuration 把秒数格式化为 mm:ss 或 h:mm:ss（供测试与展示）。
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
