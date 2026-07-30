package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-search-duckduckgo/pkg"
)

const highlightsCharBudget = 4000

type SearchArgs struct {
	Query      string `json:"query" jsonschema:"Search keywords (use English keywords for better reliability)"`
	MaxResults int    `json:"max_results,omitempty" jsonschema:"Maximum number of results to return (default 10, max 50)"`
}

type FetchArgs struct {
	URLs map[string]int `json:"urls" jsonschema:"Map of target URLs to character limits per page. Each value is clamped to [2000, 64000] and rounded to the nearest 1000. Set 0 for no limit (returns full page). Recommended: 4000 for quick extraction, 16000 for standard read, 32000 for comprehensive content."`
}

type searchResult struct {
	Title      string
	URL        string
	Snippet    string
	Highlights string
}

func main() {
	s := mcp.NewServer(&mcp.Implementation{Name: "ddg-search", Version: "1.2.0"}, nil)

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
	hReq.Header.Set("User-Agent", pkg.UA)

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
			URL:     pkg.ResolveDDGURL(href),
			Snippet: snippet,
		})
	})

	if len(results) == 0 {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "No results found."}},
		}, nil
	}

	if len(results) > maxResults {
		results = results[:maxResults]
	}

	fetchClient := &http.Client{Timeout: 15 * time.Second}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 5)

	for i := range results {
		wg.Add(1)
		go func(r *searchResult) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			content, err := pkg.FetchSingle(ctx, fetchClient, r.URL, 0)
			if err != nil {
				return
			}

			hl := pkg.ExtractHighlights(content, args.Query, highlightsCharBudget)
			if hl != "" {
				r.Highlights = hl
			}
		}(&results[i])
	}
	wg.Wait()

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
			content, err := pkg.FetchSingle(ctx, client, target, limit)
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
