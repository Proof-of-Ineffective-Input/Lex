package hook

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

// UA 共享 User-Agent。
const UA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/[IP] Safari/537.36"

var contentSelectorsByPriority = []string{
	"main",
	"article",
	"[role=main]",
	".post-content",
	".entry-content",
	".article-content",
	".content",
	"#content",
	"#main-content",
}

// HTMLHook 标准 HTML 抓取 hook，作为注册表默认兜底（Match 恒真）。
// 处理：抓取 → charset 解码 → tokenizer 主内容提取 → HTML→MD。
type HTMLHook struct{}

// Name 实现 Hook。
func (HTMLHook) Name() string { return "html" }

// Match 实现 Hook：兜底 hook，所有未匹配的 URL 都走 HTML 抓取。
func (HTMLHook) Match(target string) bool { return true }

// Fetch 实现 Hook。
func (HTMLHook) Fetch(ctx context.Context, client *http.Client, target string, limit int) (string, error) {
	data, contentType, err := readURL(ctx, client, target)
	if err != nil {
		return "", err
	}
	data = decodeToUTF8(data, contentType)

	htmlStr := string(data)
	contentHTML := ExtractMainContent(htmlStr)
	md, err := htmltomarkdown.ConvertString(contentHTML)
	if err != nil {
		return Truncate(contentHTML, limit), nil
	}
	return Truncate(md, limit), nil
}

// readURL 抓取 URL 字节与 Content-Type，带重试退避。
func readURL(ctx context.Context, client *http.Client, target string) ([]byte, string, error) {
	hReq, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return nil, "", err
	}
	hReq.Header.Set("User-Agent", UA)
	resp, err := doWithRetry(ctx, client, hReq)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	return data, resp.Header.Get("Content-Type"), err
}

// doWithRetry 带指数退避重试的请求执行，对 429/5xx 重试。
func doWithRetry(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			delay := time.Duration(300*(1<<uint(attempt-1))) * time.Millisecond
			delay += time.Duration(rand.Intn(100)) * time.Millisecond
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("unexpected status %d", resp.StatusCode)
			resp.Body.Close()
			continue
		}
		return resp, nil
	}
	return nil, lastErr
}

func decodeToUTF8(data []byte, contentType string) []byte {
	enc, name, certain := charset.DetermineEncoding(data, contentType)
	if enc == nil {
		return data
	}
	name = strings.ToLower(name)
	if name == "utf-8" || name == "" {
		return data
	}
	if !certain {
		return data
	}
	decoded, err := enc.NewDecoder().Bytes(data)
	if err != nil {
		return data
	}
	return decoded
}

// ExtractMainContent 用 tokenizer 单次扫描提取主内容区块 HTML，
// 替代 goquery 完整 DOM 构建，消除二次解析。
func ExtractMainContent(htmlStr string) string {
	for _, sel := range contentSelectorsByPriority {
		if frag, ok := extractBySelector(htmlStr, sel); ok && len(frag) > 200 {
			return frag
		}
	}
	return htmlStr
}

// extractBySelector 用 x/net/html tokenizer 定位首个匹配 selector 的元素，
// 返回其内部 HTML 片段。selector 支持 tag、.class、#id、[attr=val]。
func extractBySelector(src, selector string) (string, bool) {
	tag, wantClass, wantID, wantAttrKey, wantAttrVal, ok := parseSimpleSelector(selector)
	if !ok {
		return "", false
	}
	z := html.NewTokenizer(strings.NewReader(src))
	depth := 0
	inTarget := false
	targetDepth := 0
	var buf bytes.Buffer
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return buf.String(), inTarget && buf.Len() > 0
		case html.StartTagToken:
			t := z.Token()
			if !inTarget && matchesSelector(t, tag, wantClass, wantID, wantAttrKey, wantAttrVal) {
				inTarget = true
				targetDepth = depth + 1
				depth++
				continue
			}
			if inTarget {
				depth++
				writeToken(&buf, t)
			}
		case html.EndTagToken:
			t := z.Token()
			if inTarget {
				if depth == targetDepth {
					inTarget = false
					return buf.String(), true
				}
				depth--
				writeToken(&buf, t)
			}
		case html.SelfClosingTagToken:
			t := z.Token()
			if inTarget {
				writeToken(&buf, t)
			}
		case html.TextToken:
			if inTarget {
				buf.WriteString(z.Token().Data)
			}
		}
	}
}

func parseSimpleSelector(sel string) (tag string, wantClass, wantID, wantAttrKey, wantAttrVal string, ok bool) {
	if sel == "" {
		return "", "", "", "", "", false
	}
	// [attr=val]
	if strings.HasPrefix(sel, "[") && strings.HasSuffix(sel, "]") {
		inner := sel[1 : len(sel)-1]
		eq := strings.IndexByte(inner, '=')
		if eq < 0 {
			return "", "", "", inner, "", true
		}
		key := strings.TrimSpace(inner[:eq])
		val := strings.Trim(strings.TrimSpace(inner[eq+1:]), `"'`)
		return "", "", "", key, val, true
	}
	// .class
	if strings.HasPrefix(sel, ".") {
		return "", strings.TrimPrefix(sel, "."), "", "", "", true
	}
	// #id
	if strings.HasPrefix(sel, "#") {
		return "", "", strings.TrimPrefix(sel, "#"), "", "", true
	}
	// 裸 tag
	return sel, "", "", "", "", true
}

func matchesSelector(t html.Token, tag, wantClass, wantID, wantAttrKey, wantAttrVal string) bool {
	if tag != "" && t.Data != tag {
		return false
	}
	var classVal, idVal string
	attrVals := make(map[string]string)
	for _, a := range t.Attr {
		switch a.Key {
		case "class":
			classVal = a.Val
		case "id":
			idVal = a.Val
		default:
			attrVals[a.Key] = a.Val
		}
	}
	if wantClass != "" && !containsClass(classVal, wantClass) {
		return false
	}
	if wantID != "" && idVal != wantID {
		return false
	}
	if wantAttrKey != "" {
		v, exists := attrVals[wantAttrKey]
		if !exists || (wantAttrVal != "" && v != wantAttrVal) {
			return false
		}
	}
	return true
}

func containsClass(classAttr, want string) bool {
	for _, c := range strings.Fields(classAttr) {
		if c == want {
			return true
		}
	}
	return false
}

func writeToken(buf *bytes.Buffer, t html.Token) {
	switch t.Type {
	case html.StartTagToken:
		buf.WriteByte('<')
		buf.WriteString(t.Data)
		for _, a := range t.Attr {
			buf.WriteByte(' ')
			buf.WriteString(a.Key)
			buf.WriteString(`="`)
			buf.WriteString(a.Val)
			buf.WriteByte('"')
		}
		buf.WriteByte('>')
	case html.EndTagToken:
		buf.WriteString("</")
		buf.WriteString(t.Data)
		buf.WriteByte('>')
	case html.SelfClosingTagToken:
		buf.WriteByte('<')
		buf.WriteString(t.Data)
		for _, a := range t.Attr {
			buf.WriteByte(' ')
			buf.WriteString(a.Key)
			buf.WriteString(`="`)
			buf.WriteString(a.Val)
			buf.WriteByte('"')
		}
		buf.WriteString("/>")
	}
}
