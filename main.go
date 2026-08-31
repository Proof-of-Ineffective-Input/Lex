package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"lex/pkg"
	"lex/pkg/search"
)

const (
	defaultFetchLimit = 2000
	searchCacheTTL    = 30 * time.Minute
)

type SearchArgs struct {
	Query       string `json:"query" jsonschema:"Search keywords for duckduckgo or natural language description for Exa"`
	Description string `json:"description,omitempty" jsonschema:"Optional description or question for deep content reranking and highlight extraction. If omitted, uses query instead."`
	MaxResults  int    `json:"max_results,omitempty" jsonschema:"Number of results to return. Clamped to [5, 50]. default 10"`
	NumResults  int    `json:"numResults,omitempty" jsonschema:"Alias for max_results for Exa MCP compatibility."`
}

type FetchArgs struct {
	URLs          []string `json:"urls" jsonschema:"List of target URLs to fetch."`
	Limit         int      `json:"limit,omitempty" jsonschema:"Optional character limit applied to all URLs. Clamped to [2000, 64000] and rounded to the nearest 1000. Set 0 for no limit. Default: 2000."`
	MaxCharacters int      `json:"maxCharacters,omitempty" jsonschema:"Alias for limit for Exa MCP compatibility."`
}

type searchResult struct {
	Title      string
	URL        string
	Snippet    string
	Highlights string
	Score      float64
}

type toolSpec struct {
	name string
	desc string
	reg  func(s *mcp.Server, name, desc string)
}

var tools = []toolSpec{
	{
		name: "web_search_lex",
		desc: "Search the internet using Exa AI (with DuckDuckGo fallback). Supports natural language queries and standard search operators.",
		reg: func(s *mcp.Server, name, desc string) {
			mcp.AddTool[SearchArgs, any](s, &mcp.Tool{Name: name, Description: desc}, searchHandler)
		},
	},
	{
		name: "web_fetch_lex",
		desc: "Fetch URL content as Markdown, supporting auto-parsing for Office documents and integrated YouTube video transcripts via yt-dlp/Exa. Character limit clamped to [2000, 64000].",
		reg: func(s *mcp.Server, name, desc string) {
			mcp.AddTool[FetchArgs, any](s, &mcp.Tool{Name: name, Description: desc}, fetchHandler)
		},
	},
}

// searchCache 进程内搜索缓存：query → 结果 + 过期时间。
var searchCache = struct {
	mu      sync.Mutex
	entries map[string]cachedSearch
}{
	entries: make(map[string]cachedSearch),
}

type cachedSearch struct {
	results   []searchResult
	expiresAt time.Time
}

func main() {
	s := mcp.NewServer(&mcp.Implementation{Name: "Lex", Version: "0.7.0"}, nil)

	for _, t := range tools {
		t.reg(s, t.name, t.desc)
	}

	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		panic(err)
	}
}

func searchHandler(ctx context.Context, req *mcp.CallToolRequest, args SearchArgs) (*mcp.CallToolResult, any, error) {
	maxResults := args.MaxResults
	if maxResults <= 0 && args.NumResults > 0 {
		maxResults = args.NumResults
	}
	if maxResults <= 0 {
		maxResults = 10
	}
	maxResults = min(max(maxResults, 5), 50)

	// 搜索缓存命中直接返回
	if cached, ok := getCachedSearch(args.Query); ok {
		return formatSearchResults(cached), nil, nil
	}

	client := pkg.SharedClient
	results, _, err := search.Execute(ctx, client, args.Query, args.Description, maxResults)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
			IsError: true,
		}, nil, nil
	}

	var resList []searchResult
	for _, r := range results {
		resList = append(resList, searchResult{
			Title:      r.Title,
			URL:        r.URL,
			Snippet:    r.Snippet,
			Highlights: r.Highlights,
			Score:      r.Score,
		})
	}

	setCachedSearch(args.Query, resList)
	return formatSearchResults(resList), nil, nil
}

// getCachedSearch 读取未过期的搜索缓存。
func getCachedSearch(query string) ([]searchResult, bool) {
	searchCache.mu.Lock()
	defer searchCache.mu.Unlock()
	c, ok := searchCache.entries[query]
	if !ok {
		return nil, false
	}
	if time.Now().After(c.expiresAt) {
		delete(searchCache.entries, query)
		return nil, false
	}
	return c.results, true
}

func setCachedSearch(query string, results []searchResult) {
	searchCache.mu.Lock()
	defer searchCache.mu.Unlock()
	searchCache.entries[query] = cachedSearch{
		results:   results,
		expiresAt: time.Now().Add(searchCacheTTL),
	}
}

func formatSearchResults(results []searchResult) *mcp.CallToolResult {
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
	}
}

func fetchHandler(ctx context.Context, req *mcp.CallToolRequest, args FetchArgs) (*mcp.CallToolResult, any, error) {
	if len(args.URLs) == 0 {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "no url provided"}},
			IsError: true,
		}, nil, nil
	}

	client := pkg.SharedClient
	urls := args.URLs
	limit := args.Limit
	if limit == 0 && args.MaxCharacters > 0 {
		limit = args.MaxCharacters
	}
	if limit == 0 {
		limit = defaultFetchLimit
	}

	limits := make([]int, len(urls))
	for i := range limits {
		limits[i] = limit
	}

	fetched := pkg.FetchAll(ctx, client, urls, limits)

	var sb strings.Builder
	for i, urlStr := range urls {
		if i > 0 {
			sb.WriteString("\n\n---\n\n")
		}
		if fetched[i].Err != nil {
			sb.WriteString(fmt.Sprintf("Fetch failed: %v\n\n(from %s)", fetched[i].Err, urlStr))
			continue
		}
		sb.WriteString(fetched[i].Content)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: sb.String()}},
	}, nil, nil
}
