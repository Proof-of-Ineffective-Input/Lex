package pkg

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/PuerkitoBio/goquery"
)

const UA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.7.0.0 Safari/537.36"

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

func ResolveDDGURL(href string) string {
	if href == "" {
		return href
	}
	if strings.Contains(href, "uddg=") {
		u, err := url.Parse(href)
		if err == nil {
			if uddg := u.Query().Get("uddg"); uddg != "" {
				if decoded, err := url.QueryUnescape(uddg); err == nil {
					return decoded
				}
			}
		}
	}
	if strings.HasPrefix(href, "//") {
		return "https:" + href
	}
	return href
}

func NormalizeLimit(limit int) int {
	if limit <= 0 {
		return 0
	}
	if limit < 12000 {
		limit = 12000
	}
	if limit > 64000 {
		limit = 64000
	}
	return ((limit + 500) / 1000) * 1000
}

func ReadURL(ctx context.Context, client *http.Client, target string) ([]byte, error) {
	hReq, _ := http.NewRequestWithContext(ctx, "GET", target, nil)
	hReq.Header.Set("User-Agent", UA)
	resp, err := client.Do(hReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func ExtractMainContent(html string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return html
	}
	for _, sel := range contentSelectorsByPriority {
		selection := doc.Find(sel).First()
		if selection.Length() > 0 {
			inner, err := selection.Html()
			if err == nil && len(inner) > 200 {
				return inner
			}
		}
	}
	return html
}

func TruncateContent(content string, limit int) string {
	if limit <= 0 || len(content) <= limit {
		return content
	}
	truncated := content[:limit]
	return fmt.Sprintf("%s\n\n---\n*Content truncated to %d characters (original: %d characters)*", truncated, limit, len(content))
}

func FetchSingle(ctx context.Context, client *http.Client, target string, limit int) (string, error) {
	limit = NormalizeLimit(limit)
	data, err := ReadURL(ctx, client, target)
	if err != nil {
		return "", err
	}
	html := string(data)
	contentHTML := ExtractMainContent(html)
	md, err := htmltomarkdown.ConvertString(contentHTML)
	if err != nil {
		return TruncateContent(contentHTML, limit), nil
	}
	return TruncateContent(md, limit), nil
}
