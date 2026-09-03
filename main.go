package main

import (
	"context"
	"fmt"
	"strconv"
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
	Query      string `json:"query" jsonschema:"Natural-language query describing what you want to find. Works for both keyword sequences and full sentences."`
	MaxResults int    `json:"max_results,omitempty" jsonschema:"Number of results to return. Clamped to [5, 50]. default 10"`
}

type FetchArgs struct {
	URLs  []string `json:"urls" jsonschema:"List of target URLs to fetch."`
	Char  string   `json:"char" jsonschema:"Character budget per URL as a number (clamped to [2000, 64000], rounded to nearest 1000) or the trigger word 'full' to return the entire page without reranking."`
	Query string   `json:"query,omitempty" jsonschema:"Optional semantic focus. When provided, fetched content is re-ranked to keep the parts most relevant to this query, preserving structure and order. Ignored when char is 'full'."`
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
		name: "web_search",
		desc: "Search the web and return relevant results with embedded page highlights, not just snippets.",
		reg: func(s *mcp.Server, name, desc string) {
			mcp.AddTool[SearchArgs, any](s, &mcp.Tool{Name: name, Description: desc}, searchHandler)
		},
	},
	{
		name: "web_fetch",
		desc: "Fetch URL content as Markdown, with optional semantic re-ranking against a query. Supports Office documents and YouTube transcripts.",
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
	s := mcp.NewServer(&mcp.Implementation{Name: "Lex", Version: "0.7.2"}, nil)

	for _, t := range tools {
		t.reg(s, t.name, t.desc)
	}

	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		panic(err)
	}
}

func searchHandler(ctx context.Context, req *mcp.CallToolRequest, args SearchArgs) (*mcp.CallToolResult, any, error) {
	maxResults := args.MaxResults
	if maxResults <= 0 {
		maxResults = 10
	}
	maxResults = min(max(maxResults, 5), 50)

	// 搜索缓存命中直接返回
	if cached, ok := getCachedSearch(args.Query); ok {
		return formatSearchResults(cached), nil, nil
	}

	client := pkg.SharedClient
	results, _, err := search.Execute(ctx, client, args.Query, maxResults)
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

// parseChar 解析 fetch 的 char 参数：识别触发词 "full"（返回 0 表示不截断不 rerank），
// 否则将数字字符串转换为 int 并 clamp 到 [2000, 64000] 取整到千位。
func parseChar(s string) (int, error) {
	if strings.EqualFold(strings.TrimSpace(s), "full") {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("char must be 'full' or a number, got %q", s)
	}
	return pkg.NormalizeLimit(n), nil
}

func fetchHandler(ctx context.Context, req *mcp.CallToolRequest, args FetchArgs) (*mcp.CallToolResult, any, error) {
	if len(args.URLs) == 0 {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "no url provided"}},
			IsError: true,
		}, nil, nil
	}

	limit, err := parseChar(args.Char)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
			IsError: true,
		}, nil, nil
	}

	client := pkg.SharedClient
	urls := args.URLs

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
		content := fetched[i].Content
		// 语义定向：非 full 且提供 query 时，保序重排到预算内最符合语义的内容
		if limit != 0 && args.Query != "" {
			content = pkg.RerankByChars(content, args.Query, limit)
		}
		sb.WriteString(content)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: sb.String()}},
	}, nil, nil
}
