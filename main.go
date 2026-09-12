package main

import (
	"context"
	"encoding/json"
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

// CharArg 接受 JSON 数字或字符串，统一解析为字符串。
type CharArg string

func (c *CharArg) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*c = ""
		return nil
	}
	if strings.HasPrefix(trimmed, "\"") {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*c = CharArg(s)
		return nil
	}
	*c = CharArg(trimmed)
	return nil
}

// FetchArgs 单 URL 入参。多 URL 并行由模型自身的并行工具调用承担，
// 避免 array 型 schema 触发 Gemini function_declarations 校验失败。
type FetchArgs struct {
	URL   string  `json:"url" jsonschema:"Target URL to fetch."`
	Char  CharArg `json:"char,omitempty" jsonschema:"Character budget as a number, clamped to [2000, 64000] and rounded to nearest 1000. Defaults to 2000 when omitted."`
	Query string  `json:"query,omitempty" jsonschema:"Optional semantic focus. When provided, fetched content is re-ranked to keep the parts most relevant to this query, preserving structure and order."`
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
		desc: "Fetch a URL as Markdown, with optional semantic re-ranking against a query. Supports Office documents and YouTube transcripts. Call this tool multiple times in parallel to fetch several URLs at once.",
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
	s := mcp.NewServer(&mcp.Implementation{Name: "Lex", Version: "0.8.1"}, nil)

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

// parseChar 解析 fetch 的 char 参数：空值取默认预算，其余转换为 int 后
// clamp 到 [2000, 64000] 并取整到千位。不存在无过滤模式。
func parseChar(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return pkg.NormalizeLimit(defaultFetchLimit), nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("char must be a number, got %q", s)
	}
	return pkg.NormalizeLimit(n), nil
}

func fetchHandler(ctx context.Context, req *mcp.CallToolRequest, args FetchArgs) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(args.URL) == "" {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "no url provided"}},
			IsError: true,
		}, nil, nil
	}

	limit, err := parseChar(string(args.Char))
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
			IsError: true,
		}, nil, nil
	}

	client := pkg.SharedClient
	fetched := pkg.FetchAll(ctx, client, []string{args.URL}, []int{limit})

	if fetched[0].Err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Fetch failed: %v\n\n(from %s)", fetched[0].Err, args.URL)}},
			IsError: true,
		}, nil, nil
	}

	content := fetched[0].Content
	// 语义定向：提供 query 时，保序重排到预算内最符合语义的内容
	if args.Query != "" {
		content = pkg.RerankByChars(content, args.Query, limit)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: content}},
	}, nil, nil
}
