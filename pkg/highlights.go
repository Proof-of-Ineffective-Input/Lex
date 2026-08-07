package pkg

import "mcp-search-duckduckgo/pkg/rerank"

func ScorePage(content, query string) float64 {
	return rerank.ScorePage(content, query)
}

func ExtractHighlights(content, query string, maxTokens int) string {
	return rerank.ExtractHighlights(content, query, maxTokens)
}
