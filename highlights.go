package main

import (
	"math"
	"strings"
	"unicode"
)

type scoredSentence struct {
	Text          string
	Score         float64
	OriginalIndex int
}

// extractHighlights splits content into sentences, scores each against the
// query using keyword heuristics, re-ranks by relevance, then truncates to
// maxChars on whole-sentence boundaries.
func extractHighlights(content, query string, maxChars int) string {
	if maxChars <= 0 || query == "" || content == "" {
		return ""
	}

	sentences := splitSentencesSimple(content)
	if len(sentences) <= 2 {
		// Too few sentences to bother re-ranking; just truncate.
		return truncateSentences(sentences, maxChars)
	}

	queryTokens := tokenize(query)
	if len(queryTokens) == 0 {
		return truncateSentences(sentences, maxChars)
	}

	// Build bigrams for phrase-level matching.
	queryBigrams := buildBigrams(queryTokens)

	scored := make([]scoredSentence, len(sentences))
	maxHits := 0
	for i, s := range sentences {
		tokens := tokenize(s)
		hits := countHits(tokens, queryTokens)
		bigramHits := countBigramHits(tokens, queryBigrams)
		totalHits := hits + bigramHits
		if totalHits > maxHits {
			maxHits = totalHits
		}
		hitRate := float64(countUniqueMatches(tokens, queryTokens)) / float64(len(queryTokens))
		scored[i] = scoredSentence{
			Text:          s,
			Score:         hitRate,
			OriginalIndex: i,
		}
		_ = totalHits // used below
	}

	// Compute final score: hit_rate * 0.5 + hit_count_norm * 0.3 + position_bonus * 0.2
	for i := range scored {
		tokens := tokenize(scored[i].Text)
		hits := countHits(tokens, queryTokens) + countBigramHits(tokens, queryBigrams)
		hitCountNorm := 0.0
		if maxHits > 0 {
			hitCountNorm = math.Log(1+float64(hits)) / math.Log(1+float64(maxHits))
		}
		positionBonus := 1.0 - 0.5*float64(scored[i].OriginalIndex)/float64(len(scored))
		scored[i].Score = 0.5*scored[i].Score + 0.3*hitCountNorm + 0.2*positionBonus
	}

	// Sort by score descending.
	sorted := make([]scoredSentence, len(scored))
	copy(sorted, scored)
	// Simple insertion sort (small n).
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].Score > sorted[j-1].Score; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}

	// Select top sentences up to maxChars, keeping original order.
	selected := make(map[int]bool)
	totalChars := 0
	for _, s := range sorted {
		if s.Score <= 0 {
			continue
		}
		n := len(s.Text)
		if totalChars+n > maxChars {
			break
		}
		selected[s.OriginalIndex] = true
		totalChars += n
	}

	// If nothing selected (all scores zero), fall back to first sentences.
	if len(selected) == 0 {
		return truncateSentences(sentences, maxChars)
	}

	// Rebuild in original order.
	var b strings.Builder
	for i, s := range sentences {
		if !selected[i] {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(s)
	}
	return b.String()
}

// splitSentencesSimple splits text into sentences using punctuation and newline
// boundaries. No NLP dependency — pure heuristic.
func splitSentencesSimple(text string) []string {
	if text == "" {
		return nil
	}

	// Normalize line endings.
	text = strings.ReplaceAll(text, "\r\n", "\n")

	var sentences []string
	var buf strings.Builder

	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]

		// Paragraph break: double newline.
		if ch == '\n' && i+1 < len(runes) && runes[i+1] == '\n' {
			flushBuf(&buf, &sentences)
			// Skip the second \n.
			i++
			continue
		}

		buf.WriteRune(ch)

		// Sentence-ending punctuation.
		if isSentenceEnd(ch) {
			// Check it's not an abbreviation (followed by more lowercase / digit).
			if i+1 >= len(runes) || !isLowerOrDigit(runes[i+1]) {
				// Look ahead: if next non-space is uppercase or end, it's a boundary.
				j := i + 1
				for j < len(runes) && (runes[j] == ' ' || runes[j] == '\t') {
					j++
				}
				if j >= len(runes) || unicode.IsUpper(runes[j]) || runes[j] == '\n' {
					flushBuf(&buf, &sentences)
					i = j - 1
					continue
				}
			}
		}

		// Single newline (list item, heading).
		if ch == '\n' {
			flushBuf(&buf, &sentences)
		}
	}

	// Flush remaining.
	flushBuf(&buf, &sentences)

	// Filter and clean.
	cleaned := make([]string, 0, len(sentences))
	for _, s := range sentences {
		s = strings.TrimSpace(s)
		if len([]rune(s)) < 10 {
			continue
		}
		cleaned = append(cleaned, s)
	}
	return cleaned
}

func isSentenceEnd(ch rune) bool {
	return ch == '。' || ch == '！' || ch == '？' || ch == '；' ||
		ch == '.' || ch == '!' || ch == '?' || ch == ';'
}

func isLowerOrDigit(ch rune) bool {
	return unicode.IsLower(ch) || unicode.IsDigit(ch)
}

func flushBuf(buf *strings.Builder, sentences *[]string) {
	if buf.Len() > 0 {
		*sentences = append(*sentences, buf.String())
		buf.Reset()
	}
}

// truncateSentences returns sentences joined, truncated to maxChars on
// whole-sentence boundaries.
func truncateSentences(sentences []string, maxChars int) string {
	var b strings.Builder
	for _, s := range sentences {
		if b.Len()+len(s) > maxChars {
			break
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(s)
	}
	return b.String()
}

// tokenize lowercases and splits into words, filtering common stop words.
func tokenize(text string) []string {
	text = strings.ToLower(text)
	raw := strings.Fields(text)
	if len(raw) == 0 {
		return nil
	}
	tokens := make([]string, 0, len(raw))
	for _, w := range raw {
		// Strip leading/trailing punctuation.
		w = strings.Trim(w, `.,;:!?"'()[]{}「」『』【】《》<>、，。！？；：""''`)
		if w == "" {
			continue
		}
		if isStopWord(w) {
			continue
		}
		tokens = append(tokens, w)
	}
	return tokens
}

func isStopWord(w string) bool {
	switch w {
	case "a", "an", "the", "is", "are", "was", "were", "be", "been",
		"it", "its", "this", "that", "these", "those",
		"in", "on", "at", "to", "for", "of", "with", "by", "from",
		"and", "or", "but", "not", "no",
		"i", "you", "he", "she", "we", "they", "me", "him", "her", "us", "them",
		"my", "your", "his", "their", "our",
		"do", "does", "did", "have", "has", "had",
		"can", "could", "will", "would", "shall", "should", "may", "might",
		"的", "了", "是", "在", "和", "也", "就", "都", "而", "及",
		"与", "着", "或", "一个", "没有", "我们", "你们", "他们",
		"这个", "那个", "这些", "那些", "不", "被", "把", "从":
		return true
	}
	return false
}

// countHits returns how many tokens from queryTokens appear in sentenceTokens.
func countHits(sentenceTokens, queryTokens []string) int {
	if len(sentenceTokens) == 0 || len(queryTokens) == 0 {
		return 0
	}
	set := make(map[string]int, len(sentenceTokens))
	for _, t := range sentenceTokens {
		set[t]++
	}
	hits := 0
	for _, q := range queryTokens {
		hits += set[q]
	}
	return hits
}

// countUniqueMatches returns how many distinct query tokens appear at least once.
func countUniqueMatches(sentenceTokens, queryTokens []string) int {
	if len(sentenceTokens) == 0 || len(queryTokens) == 0 {
		return 0
	}
	set := make(map[string]bool, len(sentenceTokens))
	for _, t := range sentenceTokens {
		set[t] = true
	}
	matches := 0
	for _, q := range queryTokens {
		if set[q] {
			matches++
		}
	}
	return matches
}

// buildBigrams builds bigrams from tokens.
func buildBigrams(tokens []string) []string {
	if len(tokens) < 2 {
		return nil
	}
	bigrams := make([]string, 0, len(tokens)-1)
	for i := 0; i < len(tokens)-1; i++ {
		bigrams = append(bigrams, tokens[i]+" "+tokens[i+1])
	}
	return bigrams
}

// countBigramHits counts how many bigrams from queryBigrams appear in sentenceTokens.
func countBigramHits(sentenceTokens, queryBigrams []string) int {
	if len(sentenceTokens) < 2 || len(queryBigrams) == 0 {
		return 0
	}
	// Build sentence bigram set.
	set := make(map[string]int, len(sentenceTokens)-1)
	for i := 0; i < len(sentenceTokens)-1; i++ {
		set[sentenceTokens[i]+" "+sentenceTokens[i+1]]++
	}
	hits := 0
	for _, bg := range queryBigrams {
		hits += set[bg]
	}
	return hits
}
