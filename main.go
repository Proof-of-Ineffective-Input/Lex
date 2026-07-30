package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/PuerkitoBio/goquery"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const UA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/[IP] Safari/537.36"

// searchLimit is the default character budget for highlights per result page.
// Each result page is fetched in full, then highlights are extracted within
// this budget using keyword-heuristic sentence re-ranking.
const defaultSearchLimit = 4000

type SearchArgs struct {
	Query      string `json:"query" jsonschema:"Search keywords (use English keywords for better reliability)"`
	MaxResults int    `json:"max_results,omitempty" jsonschema:"Maximum number of results to return (default 10, max 50)"`
}

type FetchArgs struct {
	URLs map[string]int `json:"urls" jsonschema:"Map of target URLs to character limits per page. Each value is clamped to [2000, 64000] and rounded to the nearest 1000. Set 0 for no limit (returns full page). Recommended: 4000 for quick highlights, 16000 for standard read, 32000 for comprehensive content."`
}

type searchResult struct {
	Title      string
	URL        string
	Snippet    string
	Highlights string
}

func main() {
	s := mcp.NewServer(&mcp.Implementation{Name: "ddg-search", Version: "1.2.0"}, nil)

	// Use Server.AddTool (low-level API) to avoid the generic AddTool's
	// automatic StructuredContent population. The generic AddTool wraps
	// handlers via toolForErr, which marshals the second return value (Out)
	// into StructuredContent and adds a JSON-serialized TextContent fallback.
	// For plain-markdown tools, we want only Content with no StructuredContent.
	s.AddTool(&mcp.Tool{
		Name:        "search",
		Description: "Search DuckDuckGo Lite, fetch each result page, extract query-relevant highlights via keyword heuristics, and return structured results with markdown rendering",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "Search keywords (use English keywords for better reliability)"
				},
				"max_results": {
					"type": "integer",
					"description": "Maximum number of results to return (default 10, max 50)"
				}
			},
			"required": ["query"]
		}`),
	}, searchHandler)

	s.AddTool(&mcp.Tool{
		Name:        "fetch",
		Description: "Fetch URL content via direct HTML-to-Markdown conversion with optional character limits per URL. Each value is clamped to [2000, 64000] and rounded to the nearest 1000. Set 0 for no limit (returns full page).",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"urls": {
					"type": "object",
					"description": "Map of target URLs to character limits per page. Each value is clamped to [2000, 64000] and rounded to the nearest 1000. Set 0 for no limit (returns full page). Recommended: 4000 for quick extraction, 16000 for standard read, 32000 for comprehensive content.",
					"additionalProperties": {
						"type": "integer"
					}
				}
			},
			"required": ["urls"]
		}`),
	}, fetchHandler)

	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		panic(err)
	}
}

func searchHandler(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Manually unmarshal arguments.
	var args SearchArgs
	raw, err := json.Marshal(req.Params.Arguments)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("failed to marshal arguments: %v", err)}},
			IsError: true,
		}, nil
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("invalid arguments: %v", err)}},
			IsError: true,
		}, nil
	}

	maxResults := args.MaxResults
	if maxResults <= 0 {
		maxResults = 10
	}
	if maxResults > 50 {
		maxResults = 50
	}

	client := &http.Client{Timeout: 10 * time.Second}
	u := fmt.Sprintf("https://lite.duckduckgo.com/lite/?q=%s", url.QueryEscape(args.Query))

	hReq, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	hReq.Header.Set("User-Agent", UA)

	resp, err := client.Do(hReq)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
			IsError: true,
		}, nil
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
			IsError: true,
		}, nil
	}

	// Parse DDG results.
	var results []searchResult
	doc.Find("table").Last().Find("tr").Each(func(i int, s *goquery.Selection) {
		link := s.Find("a.result-link")
		if link.Length() == 0 {
			return
		}
		title := strings.TrimSpace(link.Text())
		href, _ := link.Attr("href")
		snippet := strings.TrimSpace(s.Next().Text())
		if snippet == "" {
			snippet = "(no description available)"
		}
		results = append(results, searchResult{
			Title:   title,
			URL:     resolveDDGURL(href),
			Snippet: snippet,
		})
	})

	if len(results) == 0 {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "No results found."}},
		}, nil
	}

	// Cap results to maxResults.
	if len(results) > maxResults {
		results = results[:maxResults]
	}

	// Concurrently fetch each result page and extract highlights.
	fetchClient := &http.Client{Timeout: 15 * time.Second}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 5) // concurrency 5

	for i := range results {
		wg.Add(1)
		go func(r *searchResult) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			content, err := fetchSingle(ctx, fetchClient, r.URL, 0) // limit=0 = full page
			if err != nil {
				return // keep original snippet only
			}

			hl := extractHighlights(content, args.Query, defaultSearchLimit)
			if hl != "" {
				r.Highlights = hl
			}
		}(&results[i])
	}
	wg.Wait()

	// Assemble output.
	var sb strings.Builder
	for i, r := range results {
		if i > 0 {
			sb.WriteString("\n\n---\n\n")
		}
		sb.WriteString(fmt.Sprintf("Title: %s\nURL: %s\nSnippet: %s", r.Title, r.URL, r.Snippet))
		if r.Highlights != "" {
			sb.WriteString(fmt.Sprintf("\nHighlights:\n%s", r.Highlights))
		}
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: sb.String()}},
	}, nil
}

func fetchHandler(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Manually unmarshal arguments.
	var args FetchArgs
	raw, err := json.Marshal(req.Params.Arguments)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("failed to marshal arguments: %v", err)}},
			IsError: true,
		}, nil
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("invalid arguments: %v", err)}},
			IsError: true,
		}, nil
	}

	if len(args.URLs) == 0 {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "no url provided"}},
			IsError: true,
		}, nil
	}

	client := &http.Client{Timeout: 30 * time.Second}
	results := make(map[string]string, len(args.URLs))
	errs := make(map[string]error, len(args.URLs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 5)
	urls := make([]string, 0, len(args.URLs))
	for u := range args.URLs {
		urls = append(urls, u)
	}
	sort.Strings(urls)
	for _, urlStr := range urls {
		limit := args.URLs[urlStr]
		wg.Add(1)
		go func(target string, limit int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			content, err := fetchSingle(ctx, client, target, limit)
			if err != nil {
				errs[target] = err
				return
			}
			results[target] = content
		}(urlStr, limit)
	}
	wg.Wait()

	var sb strings.Builder
	first := true
	for _, urlStr := range urls {
		if !first {
			sb.WriteString("\n\n---\n\n")
		}
		first = false
		if err, ok := errs[urlStr]; ok {
			sb.WriteString(fmt.Sprintf("Fetch failed: %v\n\n(from %s)", err, urlStr))
			continue
		}
		sb.WriteString(results[urlStr])
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: sb.String()}},
	}, nil
}

func resolveDDGURL(href string) string {
	if href == "" {
		return href
	}
	// DuckDuckGo Lite wraps URLs in //duckduckgo.com/l/?uddg=<encoded>&rut=<hash>
	// Extract the uddg parameter to get the real URL.
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
	// Protocol-relative URL: prepend https
	if strings.HasPrefix(href, "//") {
		return "https:" + href
	}
	return href
}

func normalizeLimit(limit int) int {
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

func fetchSingle(ctx context.Context, client *http.Client, target string, limit int) (string, error) {
	limit = normalizeLimit(limit)
	data, err := readURL(ctx, client, target)
	if err != nil {
		return "", err
	}

	html := string(data)

	// Try to extract main content area to avoid nav/header/footer noise.
	contentHTML := extractMainContent(html)

	md, err := htmltomarkdown.ConvertString(contentHTML)
	if err != nil {
		return truncateContent(contentHTML, limit), nil
	}
	return truncateContent(md, limit), nil
}

func truncateContent(content string, limit int) string {
	if limit <= 0 || len(content) <= limit {
		return content
	}
	truncated := content[:limit]
	return fmt.Sprintf("%s\n\n---\n*Content truncated to %d characters (original: %d characters)*", truncated, limit, len(content))
}

// extractMainContent attempts to extract the primary content area from HTML
// using common selectors. Falls back to full HTML if no content area is found.
func extractMainContent(html string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return html
	}

	// Ordered by specificity: most semantic first.
	selectors := []string{
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

	for _, sel := range selectors {
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

func readURL(ctx context.Context, client *http.Client, target string) ([]byte, error) {
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
