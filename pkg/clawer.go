package pkg

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/PuerkitoBio/goquery"
	"github.com/hashicorp/golang-lru/v2/expirable"
	"golang.org/x/net/html/charset"
)

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

var (
	fetchCache *expirable.LRU[string, string]
	cacheOnce  sync.Once
)

func getCache() *expirable.LRU[string, string] {
	cacheOnce.Do(func() {
		fetchCache = expirable.NewLRU[string, string](256, nil, 5*time.Minute)
	})
	return fetchCache
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
	if limit < 2000 {
		limit = 2000
	}
	if limit > 64000 {
		limit = 64000
	}
	return ((limit + 500) / 1000) * 1000
}

func ReadURL(ctx context.Context, client *http.Client, target string) ([]byte, string, error) {
	hReq, _ := http.NewRequestWithContext(ctx, "GET", target, nil)
	hReq.Header.Set("User-Agent", UA)
	resp, err := client.Do(hReq)
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

func decodeToUTF8(data []byte, contentType string) []byte {
	enc, name, certain := charset.DetermineEncoding(data, contentType)
	if enc == nil {
		return data
	}
	name = strings.ToLower(name)
	if name == "utf-8" || name == "" {
		return data
	}
	// certain=false means the detector is guessing; skip non-certain non-utf8 to avoid corrupting valid utf-8
	if !certain {
		return data
	}
	decoded, err := enc.NewDecoder().Bytes(data)
	if err != nil {
		return data
	}
	return decoded
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
	cacheKey := fmt.Sprintf("%s|%d", target, limit)

	cache := getCache()
	if cached, ok := cache.Get(cacheKey); ok {
		return cached, nil
	}

	data, contentType, err := ReadURL(ctx, client, target)
	if err != nil {
		return "", err
	}

	data = decodeToUTF8(data, contentType)

	html := string(data)
	contentHTML := ExtractMainContent(html)
	md, err := htmltomarkdown.ConvertString(contentHTML)
	if err != nil {
		result := TruncateContent(contentHTML, limit)
		cache.Add(cacheKey, result)
		return result, nil
	}
	result := TruncateContent(md, limit)
	cache.Add(cacheKey, result)
	return result, nil
}
