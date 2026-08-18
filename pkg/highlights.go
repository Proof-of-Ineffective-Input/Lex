package pkg

import "lex/pkg/rerank"

// PageAnalysis 一次评分后的页面分析结果，供 ExtractHighlightsFromAnalysis 复用。
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

// ExtractHighlightsFromAnalysis 基于已有分析结果提取高亮，复用 Tokenize。
func ExtractHighlightsFromAnalysis(a *PageAnalysis, query string, maxTokens int) string {
	if a == nil {
		return ""
	}
	return rerank.ExtractHighlights(a.content, a.query, maxTokens)
}
