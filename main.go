package main

import (
	"context"
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
	"mcp-search-duckduckgo/pkg/hook"
)

const (
	defaultFetchLimit    = 2000
	highlightsFullBudget = 3000
	searchCacheTTL       = 30 * time.Minute
)

type SearchArgs struct {
	Query      string `json:"query" jsonschema:"Search keywords (use English keywords for better reliability)"`
	MaxResults int    `json:"max_results,omitempty" jsonschema:"Number of results to return. Clamped to [5, 50]. Recommended: 10-20 for balanced speed and coverage (default 10)"`
}

type FetchArgs struct {
	URLs map[string]int `json:"urls" jsonschema:"Map of target URLs to character limits per page. Each value is clamped to [2000, 64000] and rounded to the nearest 1000. Set 0 for no limit (returns full page). Recommended: 2000 for quick extraction (~500 tokens), 8000 for standard read, 16000 for comprehensive content."`
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
		desc: "Search DuckDuckGo Lite, fetch each result page, extract query-relevant highlights via BM25 re-ranking, and return structured results with markdown rendering. Also callable as 'search' or 'web_search'. Top-ranked pages get full highlights; lower-ranked pages get condensed highlights.",
		reg: func(s *mcp.Server, name, desc string) {
			mcp.AddTool[SearchArgs, any](s, &mcp.Tool{Name: name, Description: desc}, searchHandler)
		},
	},
	{
		name: "web_fetch_lex",
		desc: "Fetch URL content via direct HTML-to-Markdown conversion with optional character limits per URL. Each value is clamped to [2000, 64000] and rounded to the nearest 1000. Set 0 for no limit (returns full page). Also callable as 'fetch' or 'web_fetch'.",
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
	s := mcp.NewServer(&mcp.Implementation{Name: "Lex", Version: "0.6.0"}, nil)

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
	u := fmt.Sprintf("https://lite.duckduckgo.com/lite/?q=%s", url.QueryEscape(args.Query))

	hReq, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	hReq.Header.Set("User-Agent", hook.UA)
	hReq.Header.Set("Referer", "https://html.duckduckgo.com/")
	hReq.Header.Set("Sec-Fetch-Site", "same-origin")

	resp, err := client.Do(hReq)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
			IsError: true,
		}, nil, nil
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
			IsError: true,
		}, nil, nil
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
		}, nil, nil
	}

	if len(results) > maxResults {
		results = results[:maxResults]
	}

	// per-host 限流并发抓取
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)

	contents := make([]string, len(results))
	for i := range results {
		wg.Add(1)
		go func(idx int, r *searchResult) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			release := pkg.AcquireHost(pkg.HostOf(r.URL))
			defer release()
			content, err := pkg.FetchSingle(ctx, client, r.URL, defaultFetchLimit)
			if err != nil {
				return
			}
			contents[idx] = content
		}(i, &results[i])
	}
	wg.Wait()

	// 评分：共用一次 Tokenize 分析结果
	analyses := make([]*pkg.PageAnalysis, len(results))
	for i := range results {
		if contents[i] != "" {
			analyses[i] = pkg.AnalyzePage(contents[i], args.Query)
			results[i].Score = analyses[i].Score
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	// 高亮并行化
	n := len(results)
	topN := (n + 1) / 2 // 前一半（含中间）
	var hlWg sync.WaitGroup
	hlSem := make(chan struct{}, 8)
	for i := range results {
		if contents[i] == "" {
			continue
		}
		hlWg.Add(1)
		go func(idx int) {
			defer hlWg.Done()
			hlSem <- struct{}{}
			defer func() { <-hlSem }()
			budget := highlightsFullBudget
			if idx >= topN {
				budget = highlightsFullBudget / 2
			}
			hl := pkg.ExtractHighlightsFromAnalysis(analyses[idx], args.Query, budget)
			if hl != "" {
				results[idx].Highlights = hl
			}
		}(i)
	}
	hlWg.Wait()

	// 缓存结果（不含 highlights 过期变体，缓存原始排序与分数）
	setCachedSearch(args.Query, results)
	return formatSearchResults(results), nil, nil
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
	results := make(map[string]string, len(args.URLs))
	errs := make(map[string]error, len(args.URLs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
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
			release := pkg.AcquireHost(pkg.HostOf(target))
			defer release()
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
	}, nil, nil
}
