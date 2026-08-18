package pkg

import "mcp-search-duckduckgo/pkg/rerank"

// PageAnalysis 一次 Tokenize 分析结果，供 ScorePage 与 ExtractHighlights 复用。
type PageAnalysis struct {
	Score float64
	// 内部缓存：句级统计与 tokens，避免重复 Tokenize
	content string
	query   string
}

// AnalyzePage 对内容做一次分析，返回可复用的 PageAnalysis。
func AnalyzePage(content, query string) *PageAnalysis {
	return &PageAnalysis{
		Score:   rerank.ScorePage(content, query),
		content: content,
		query:   query,
	}
}

// ScorePage 独立评分入口（无复用场景）。
func ScorePage(content, query string) float64 {
	return rerank.ScorePage(content, query)
}

// ExtractHighlightsFromAnalysis 基于已有分析结果提取高亮，复用 Tokenize。
func ExtractHighlightsFromAnalysis(a *PageAnalysis, query string, maxTokens int) string {
	if a == nil {
		return ""
	}
	return rerank.ExtractHighlights(a.content, a.query, maxTokens)
}

// ExtractHighlights 独立高亮入口（无复用场景）。
func ExtractHighlights(content, query string, maxTokens int) string {
	return rerank.ExtractHighlights(content, query, maxTokens)
}
