package main

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
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const UA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/111.0.0.0 Safari/537.36"

type SearchArgs struct {
	Query string `json:"query" jsonschema:"Search keywords (use English keywords for better reliability)"`
}

type URLToken struct {
	URL   string `json:"url" jsonschema:"Target URL to scrape"`
	Limit int    `json:"limit" jsonschema:"Content limit in characters (recommended 4000-12000, 0 for no limit)"`
}

type FetchArgs struct {
	URLs []URLToken `json:"urls" jsonschema:"Target URLs to scrape with optional character limits"`
}

type SearchResult struct {
	Markdown string `json:"markdown" jsonschema:"Search results in markdown format"`
}

type FetchResult struct {
	Content string `json:"content" jsonschema:"Fetched content in markdown format"`
}

func main() {
	s := mcp.NewServer(&mcp.Implementation{Name: "ddg-search", Version: "1.0.0"}, nil)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "search",
		Description: "Search DuckDuckGo Lite and return results as markdown (use English keywords for better reliability)",
	}, search)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "fetch",
		Description: "Fetch URL content via Jina or direct HTML-to-MD conversion with optional character limits per URL",
	}, fetch)

	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		panic(err)
	}
}

func search(ctx context.Context, req *mcp.CallToolRequest, args SearchArgs) (*mcp.CallToolResult, SearchResult, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	u := fmt.Sprintf("https://lite.duckduckgo.com/lite/?q=%s", url.QueryEscape(args.Query))

	hReq, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	hReq.Header.Set("User-Agent", UA)

	resp, err := client.Do(hReq)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
			IsError: true,
		}, SearchResult{}, nil
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
			IsError: true,
		}, SearchResult{}, nil
	}

	var sb strings.Builder
	doc.Find("table").Last().Find("tr").Each(func(i int, s *goquery.Selection) {
		link := s.Find("a.result-link")
		if link.Length() > 0 {
			title := strings.TrimSpace(link.Text())
			href, _ := link.Attr("href")
			snippet := strings.TrimSpace(s.Next().Text())
			sb.WriteString(fmt.Sprintf("### [%s](%s)\n%s\n\n", title, href, snippet))
		}
	})

	return nil, SearchResult{Markdown: sb.String()}, nil
}

func fetch(ctx context.Context, req *mcp.CallToolRequest, args FetchArgs) (*mcp.CallToolResult, FetchResult, error) {
	if len(args.URLs) == 0 {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "no url provided"}},
			IsError: true,
		}, FetchResult{}, nil
	}

	client := &http.Client{Timeout: 30 * time.Second}
	results := make([]string, len(args.URLs))
	errs := make([]error, len(args.URLs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 5)
	for i, target := range args.URLs {
		wg.Add(1)
		go func(i int, target URLToken) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			content, err := fetchSingle(ctx, client, target.URL, target.Limit)
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = content
		}(i, target)
	}
	wg.Wait()

	var sb strings.Builder
	for i, target := range args.URLs {
		if errs[i] != nil {
			sb.WriteString(fmt.Sprintf("### [%s](%s)\nFetch failed: %v\n\n", target.URL, target.URL, errs[i]))
			continue
		}
		sb.WriteString(fmt.Sprintf("### [%s](%s)\n%s\n\n", target.URL, target.URL, results[i]))
	}

	return nil, FetchResult{Content: sb.String()}, nil
}

func fetchSingle(ctx context.Context, client *http.Client, target string, limit int) (string, error) {
	jinaURL := fmt.Sprintf("https://r.jina.ai/%s", target)
	if data, err := readURL(ctx, client, jinaURL); err == nil {
		return truncateContent(string(data), limit), nil
	}
	data, err := readURL(ctx, client, target)
	if err != nil {
		return "", err
	}
	md, err := htmltomarkdown.ConvertString(string(data))
	if err != nil {
		return truncateContent(string(data), limit), nil
	}
	return truncateContent(md, limit), nil
}

func truncateContent(content string, limit int) string {
	if limit <= 0 || len(content) <= limit {
		return content
	}
	// 截断内容并在末尾添加截断提示
	truncated := content[:limit]
	return fmt.Sprintf("%s\n\n---\n*Content truncated to %d characters (original: %d characters)*", truncated, limit, len(content))
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
