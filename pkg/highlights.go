// Package pkg 的 BM25 重排序引擎已下沉至 pkg/rerank。
// 本文件保留对外 API 兼容的薄封装。
package pkg

import "mcp-search-duckduckgo/pkg/rerank"

// ScorePage 页面级 BM25 评分，用于 search 结果分级。
func ScorePage(content, query string) float64 {
	return rerank.ScorePage(content, query)
}

// ExtractHighlights 按 token 预算对句子做 BM25 rerank。
func ExtractHighlights(content, query string, maxTokens int) string {
	return rerank.ExtractHighlights(content, query, maxTokens)
}
