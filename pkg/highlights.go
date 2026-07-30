package pkg

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

func ExtractHighlights(content, query string, maxTokens int) string {
	if maxTokens <= 0 || query == "" || content == "" {
		return ""
	}

	sentences := splitSentencesPreservingCode(content)
	if len(sentences) <= 2 {
		return truncateByTokens(sentences, maxTokens)
	}

	queryTokens := tokenize(query)
	if len(queryTokens) == 0 {
		return truncateByTokens(sentences, maxTokens)
	}

	queryStems := stemTokens(queryTokens)
	queryBigrams := buildBigrams(queryTokens)

	isCodeQ := isCodeQuery(query)

	scored := make([]scoredSentence, len(sentences))
	maxHits := 0
	for i, s := range sentences {
		tokens := tokenize(s)
		hits := countHits(tokens, queryTokens) + countBigramHits(tokens, queryBigrams)
		hits += countStemHits(tokens, queryStems)
		if hits > maxHits {
			maxHits = hits
		}
		hitRate := float64(countUniqueMatches(tokens, queryTokens)) / float64(len(queryTokens))
		scored[i] = scoredSentence{
			Text:          s,
			Score:         hitRate,
			OriginalIndex: i,
		}
	}

	for i := range scored {
		tokens := tokenize(scored[i].Text)
		hits := countHits(tokens, queryTokens) + countBigramHits(tokens, queryBigrams)
		hits += countStemHits(tokens, queryStems)
		hitCountNorm := 0.0
		if maxHits > 0 {
			hitCountNorm = math.Log(1+float64(hits)) / math.Log(1+float64(maxHits))
		}
		positionBonus := 1.0 - 0.5*float64(scored[i].OriginalIndex)/float64(len(scored))

		lexicalWeight := 0.5
		semanticWeight := 0.3
		if isCodeQ {
			lexicalWeight = 0.65
			semanticWeight = 0.15
		}

		codeBoost := 0.0
		if containsCodeBlock(scored[i].Text) {
			codeBoost = 0.1
		}

		noisePenalty := 0.0
		if isNoiseSentence(scored[i].Text) {
			noisePenalty = 0.3
		}

		scored[i].Score = lexicalWeight*scored[i].Score +
			semanticWeight*hitCountNorm +
			0.2*positionBonus +
			codeBoost -
			noisePenalty
	}

	sorted := make([]scoredSentence, len(scored))
	copy(sorted, scored)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].Score > sorted[j-1].Score; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}

	selected := make(map[int]bool)
	totalTokens := 0
	for _, s := range sorted {
		if s.Score <= 0 {
			continue
		}
		n := estimateTokens(s.Text)
		if totalTokens+n > maxTokens {
			break
		}
		selected[s.OriginalIndex] = true
		totalTokens += n
	}

	if len(selected) == 0 {
		return truncateByTokens(sentences, maxTokens)
	}

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

func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	runes := []rune(s)
	cjkCount := 0
	for _, r := range runes {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			cjkCount++
		}
	}
	nonCJK := len(runes) - cjkCount
	tokens := float64(cjkCount)/1.5 + float64(nonCJK)/4.0
	if tokens < 1 {
		tokens = 1
	}
	return int(math.Ceil(tokens))
}

func truncateByTokens(sentences []string, maxTokens int) string {
	var b strings.Builder
	used := 0
	for _, s := range sentences {
		n := estimateTokens(s)
		if used+n > maxTokens {
			break
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(s)
		used += n
	}
	return b.String()
}

func splitSentencesPreservingCode(text string) []string {
	if text == "" {
		return nil
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")

	var blocks []string
	var buf strings.Builder
	inCodeBlock := false
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inCodeBlock {
				buf.WriteString(line)
				buf.WriteByte('\n')
				blocks = append(blocks, buf.String())
				buf.Reset()
				inCodeBlock = false
			} else {
				if buf.Len() > 0 {
					blocks = append(blocks, buf.String())
					buf.Reset()
				}
				buf.WriteString(line)
				buf.WriteByte('\n')
				inCodeBlock = true
			}
			continue
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	if buf.Len() > 0 {
		blocks = append(blocks, buf.String())
	}

	var sentences []string
	for _, block := range blocks {
		if strings.HasPrefix(strings.TrimSpace(block), "```") {
			s := strings.TrimSpace(block)
			if len([]rune(s)) >= 10 {
				sentences = append(sentences, s)
			}
			continue
		}
		split := splitSentencesSimple(block)
		sentences = append(sentences, split...)
	}
	return sentences
}

func splitSentencesSimple(text string) []string {
	if text == "" {
		return nil
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")

	var sentences []string
	var buf strings.Builder
	runes := []rune(text)

	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		if ch == '\n' && i+1 < len(runes) && runes[i+1] == '\n' {
			flushBuf(&buf, &sentences)
			i++
			continue
		}
		buf.WriteRune(ch)
		if isSentenceEnd(ch) {
			if i+1 >= len(runes) || !isLowerOrDigit(runes[i+1]) {
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
		if ch == '\n' {
			flushBuf(&buf, &sentences)
		}
	}
	flushBuf(&buf, &sentences)

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

func tokenize(text string) []string {
	text = strings.ToLower(text)
	raw := strings.Fields(text)
	if len(raw) == 0 {
		return nil
	}
	tokens := make([]string, 0, len(raw)*2)
	for _, w := range raw {
		w = strings.Trim(w, `.,;:!?"'()[]{}「」『』【】《》<>、，。！？；：""''`)
		if w == "" {
			continue
		}
		parts := splitIdentifier(w)
		for _, p := range parts {
			if p == "" {
				continue
			}
			if isStopWord(p) {
				continue
			}
			tokens = append(tokens, p)
		}
	}
	return tokens
}

func splitIdentifier(word string) []string {
	if strings.Contains(word, "_") {
		parts := strings.Split(word, "_")
		for i := range parts {
			parts[i] = strings.ToLower(parts[i])
		}
		return parts
	}

	var parts []string
	var buf strings.Builder
	runes := []rune(word)
	for i, r := range runes {
		if unicode.IsUpper(r) {
			if buf.Len() > 0 {
				parts = append(parts, strings.ToLower(buf.String()))
				buf.Reset()
			}
			buf.WriteRune(r)
		} else {
			if buf.Len() == 1 && unicode.IsUpper(runes[i-1]) && i+1 < len(runes) && unicode.IsLower(runes[i+1]) {
				parts = append(parts, strings.ToLower(buf.String()))
				buf.Reset()
				buf.WriteRune(r)
			} else {
				buf.WriteRune(r)
			}
		}
	}
	if buf.Len() > 0 {
		parts = append(parts, strings.ToLower(buf.String()))
	}
	if len(parts) == 0 {
		parts = []string{strings.ToLower(word)}
	}
	return parts
}

func stemTokens(tokens []string) []string {
	if len(tokens) == 0 {
		return nil
	}
	stems := make([]string, len(tokens))
	for i, t := range tokens {
		stems[i] = stem(t)
	}
	return stems
}

func stem(word string) string {
	if len(word) < 4 {
		return word
	}
	suffixes := []string{
		"ation", "ition", "ment", "ness", "less",
		"able", "ible", "al", "ial", "ful", "ous",
		"ings", "ions", "ents",
		"ing", "ion", "ent", "ive", "ize", "ise",
		"ly", "ed", "es", "er", "est",
		"s",
	}
	lower := strings.ToLower(word)
	for _, suf := range suffixes {
		if strings.HasSuffix(lower, suf) && len(lower)-len(suf) >= 3 {
			return lower[:len(lower)-len(suf)]
		}
	}
	return lower
}

func countStemHits(sentenceTokens, queryStems []string) int {
	if len(sentenceTokens) == 0 || len(queryStems) == 0 {
		return 0
	}
	stemSet := make(map[string]int, len(sentenceTokens))
	for _, t := range sentenceTokens {
		s := stem(t)
		stemSet[s]++
	}
	hits := 0
	for _, qs := range queryStems {
		hits += stemSet[qs]
	}
	return hits
}

func isCodeQuery(query string) bool {
	codeIndicators := []string{
		"::", "->", "=>", "func ", "func(", "def ", "class ", "import ",
		"package ", "fn ", "fn(", "function ", "var ", "let ", "const ",
		"type ", "interface ", "struct ", "enum ", "trait ",
		".go", ".rs", ".py", ".js", ".ts", ".c", ".h", ".cpp",
		"()", "[]", "{}",
	}
	q := strings.ToLower(query)
	for _, ind := range codeIndicators {
		if strings.Contains(q, ind) {
			return true
		}
	}
	parts := strings.Fields(q)
	for _, p := range parts {
		if strings.Contains(p, "_") || strings.Contains(p, ".") {
			return true
		}
		hasUpper := false
		hasLower := false
		for _, r := range p {
			if unicode.IsUpper(r) {
				hasUpper = true
			}
			if unicode.IsLower(r) {
				hasLower = true
			}
		}
		if hasUpper && hasLower {
			return true
		}
	}
	return false
}

func isNoiseSentence(s string) bool {
	low := strings.ToLower(s)
	noisePatterns := []string{
		"cookie", "privacy policy", "terms of service", "subscribe",
		"newsletter", "sign up", "sign in", "log in", "log out",
		"all rights reserved", "copyright", "advertisement", "sponsored",
		"click here", "read more", "related articles", "you may also like",
		"share this", "tweet", "facebook", "instagram", "linkedin",
		"navigation", "menu", "breadcrumb", "skip to content",
		"table of contents", "on this page", "in this article",
		"follow us", "contact us", "about us", "search...",
		"loading", "please enable javascript",
	}
	for _, p := range noisePatterns {
		if strings.Contains(low, p) {
			return true
		}
	}
	runes := []rune(strings.TrimSpace(s))
	if len(runes) < 15 {
		return true
	}
	return false
}

func containsCodeBlock(s string) bool {
	return strings.Contains(s, "```")
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

func countBigramHits(sentenceTokens, queryBigrams []string) int {
	if len(sentenceTokens) < 2 || len(queryBigrams) == 0 {
		return 0
	}
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
