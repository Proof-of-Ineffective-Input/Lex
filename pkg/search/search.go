package search

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// SearchResult 描述一条搜索结果。
type SearchResult struct {
	Title      string
	URL        string
	Snippet    string
	Highlights string
	Score      float64
}

// Searcher 抽象一种搜索引擎实现。
type Searcher interface {
	Name() string
	Search(ctx context.Context, client *http.Client, query string, maxResults int) ([]SearchResult, error)
}

var chain []Searcher

// Register 注册搜索 Hook。先注册者优先。
func Register(s Searcher) {
	chain = append(chain, s)
}

// ClearRegistry 重置注册表（主要用于测试）
func ClearRegistry() {
	chain = nil
}

// Execute 依次尝试注册表中的 Searcher，首个成功返回非空结果的输出；若全部失败则返回最后一个错误。
func Execute(ctx context.Context, client *http.Client, query string, maxResults int) ([]SearchResult, string, error) {
	var lastErr error
	for _, s := range chain {
		results, err := s.Search(ctx, client, query, maxResults)
		if err == nil && len(results) > 0 {
			return results, s.Name(), nil
		}
		if err != nil {
			lastErr = err
		}
	}
	if lastErr != nil {
		return nil, "", lastErr
	}
	return nil, "", fmt.Errorf("all search engines returned empty results")
}

// FormatResults 统一格式化搜索结果为 Markdown。
func FormatResults(results []SearchResult) string {
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
	return sb.String()
}
