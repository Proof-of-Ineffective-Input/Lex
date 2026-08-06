// Package rerank 提供独立的 BM25 句子重排序引擎。
// 从 pkg 下沉，供 pkg 与 pkg/hook 共同使用，避免循环依赖。
package rerank

import (
	"math"
	"strings"
	"unicode"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

var stopWords = map[string]struct{}{
	"a": {}, "an": {}, "the": {}, "is": {}, "are": {}, "was": {}, "were": {}, "be": {}, "been": {},
	"it": {}, "its": {}, "this": {}, "that": {}, "these": {}, "those": {},
	"in": {}, "on": {}, "at": {}, "to": {}, "for": {}, "of": {}, "with": {}, "by": {}, "from": {},
	"and": {}, "or": {}, "but": {}, "not": {}, "no": {},
	"i": {}, "you": {}, "he": {}, "she": {}, "we": {}, "they": {}, "me": {}, "him": {}, "her": {}, "us": {}, "them": {},
	"my": {}, "your": {}, "his": {}, "their": {}, "our": {},
	"do": {}, "does": {}, "did": {}, "have": {}, "has": {}, "had": {},
	"can": {}, "could": {}, "will": {}, "would": {}, "shall": {}, "should": {}, "may": {}, "might": {},
	"的": {}, "了": {}, "是": {}, "在": {}, "和": {}, "也": {}, "就": {}, "都": {}, "而": {}, "及": {},
	"与": {}, "着": {}, "或": {}, "一个": {}, "没有": {}, "我们": {}, "你们": {}, "他们": {},
	"这个": {}, "那个": {}, "这些": {}, "那些": {}, "不": {}, "被": {}, "把": {}, "从": {},
}

var noisePatterns = []string{
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

var garbledDecoders = []struct {
	name string
	enc  encoding.Encoding
}{
	{"gbk", simplifiedchinese.GBK},
	{"gb2312", simplifiedchinese.HZGB2312},
	{"big5", traditionalchinese.Big5},
	{"shift_jis", japanese.ShiftJIS},
	{"euc_jp", japanese.EUCJP},
	{"euc_kr", korean.EUCKR},
	{"iso_8859_1", charmap.ISO8859_1},
	{"windows_1252", charmap.Windows1252},
}

func ScorePage(content, query string) float64 {
	if content == "" || query == "" {
		return 0
	}
	queryTokens := Tokenize(query)
	if len(queryTokens) == 0 {
		return 0
	}
	queryStems := stemTokens(queryTokens)
	queryExpanded := expandIdentifierTokens(queryTokens)

	contentTokens := Tokenize(content)
	if len(contentTokens) == 0 {
		return 0
	}

	docLen := max(len(contentTokens), 1)
	tf := make(map[string]int)
	seen := make(map[string]bool)
	for _, t := range contentTokens {
		tf[t]++
		if !seen[t] {
			seen[t] = true
		}
	}
	expanded := expandIdentifierTokens(contentTokens)
	for _, et := range expanded {
		if !seen[et] {
			seen[et] = true
		}
	}

	totalDocs := 10
	bm25Score := 0.0
	avgDocLen := 200.0

	allQueryTokens := append([]string{}, queryTokens...)
	allQueryTokens = append(allQueryTokens, queryExpanded...)

	seenQt := make(map[string]bool)
	for _, qt := range allQueryTokens {
		if seenQt[qt] {
			continue
		}
		seenQt[qt] = true
		tfVal := tf[qt]
		if tfVal == 0 {
			continue
		}
		df := 1
		idf := math.Log(1 + (float64(totalDocs)-float64(df)+0.5)/(float64(df)+0.5))
		bm25Score += idf * (float64(tfVal) * (bm25K1 + 1)) /
			(float64(tfVal) + bm25K1*(1-bm25B+bm25B*float64(docLen)/avgDocLen))
	}

	for _, t := range contentTokens {
		s := stem(t)
		for _, qs := range queryStems {
			if s == qs {
				bm25Score += 0.3
				break
			}
		}
	}
	for _, et := range expanded {
		for _, qe := range queryExpanded {
			if et == qe {
				bm25Score += 0.3
				break
			}
		}
	}

	return bm25Score
}

const (
	bm25K1 = 1.5
	bm25B  = 0.75
)

type scoredSentence struct {
	Text          string
	Score         float64
	OriginalIndex int
}

// ExtractHighlights 按 token 预算对句子做 BM25 rerank（原 pkg.ExtractHighlights）。
func ExtractHighlights(content, query string, maxTokens int) string {
	return extractByBudget(content, query, maxTokens, true)
}

// RerankByChars 按字符预算对句子做 BM25 rerank，query 为空时退化为顺序截断。
func RerankByChars(content, query string, maxChars int) string {
	return extractByBudget(content, query, maxChars, false)
}

func extractByBudget(content, query string, budget int, byToken bool) string {
	if budget <= 0 || content == "" {
		return ""
	}

	sentences := splitSentencesPreservingCode(content)

	repaired := make([]string, 0, len(sentences))
	for _, s := range sentences {
		if isGarbledText(s) {
			if fixed := tryFixGarbled(s); fixed != "" {
				repaired = append(repaired, fixed)
			}

		} else {
			repaired = append(repaired, s)
		}
	}
	sentences = repaired

	if len(sentences) <= 2 {
		return truncateByBudget(sentences, budget, byToken)
	}

	queryTokens := Tokenize(query)
	if len(queryTokens) == 0 {
		return truncateByBudget(sentences, budget, byToken)
	}

	queryStems := stemTokens(queryTokens)
	queryBigrams := buildBigrams(queryTokens)
	queryExpanded := expandIdentifierTokens(queryTokens)

	type docStats struct {
		length int
		tf     map[string]int
	}
	sentenceStats := make([]docStats, len(sentences))
	totalDocs := len(sentences)
	docFreq := make(map[string]int)
	for i, s := range sentences {
		tokens := Tokenize(s)
		stats := docStats{length: len(tokens), tf: make(map[string]int)}
		seen := make(map[string]bool)
		for _, t := range tokens {
			stats.tf[t]++
			if !seen[t] {
				seen[t] = true
				docFreq[t]++
			}
		}

		expanded := expandIdentifierTokens(tokens)
		for _, et := range expanded {
			if !seen[et] {
				seen[et] = true
				docFreq[et]++
			}
		}
		sentenceStats[i] = stats
	}
	avgDocLen := 0
	for _, st := range sentenceStats {
		avgDocLen += st.length
	}
	if totalDocs > 0 {
		avgDocLen /= totalDocs
	}
	avgDocLen = max(avgDocLen, 1)

	scored := make([]scoredSentence, len(sentences))
	maxBM25 := 0.0
	for i, s := range sentences {
		tokens := Tokenize(s)
		expanded := expandIdentifierTokens(tokens)

		bm25Score := 0.0
		allQueryTokens := append([]string{}, queryTokens...)
		allQueryTokens = append(allQueryTokens, queryExpanded...)

		seen := make(map[string]bool)
		for _, qt := range allQueryTokens {
			if seen[qt] {
				continue
			}
			seen[qt] = true
			tf := sentenceStats[i].tf[qt]
			if tf == 0 {
				continue
			}
			df := docFreq[qt]
			if df == 0 {
				df = 1
			}
			idf := math.Log(1 + (float64(totalDocs)-float64(df)+0.5)/(float64(df)+0.5))
			docLen := max(sentenceStats[i].length, 1)
			bm25Score += idf * (float64(tf) * (bm25K1 + 1)) /
				(float64(tf) + bm25K1*(1-bm25B+bm25B*float64(docLen)/float64(avgDocLen)))
		}

		bigramHits := countBigramHits(tokens, queryBigrams)
		stemHits := countStemHits(tokens, queryStems)
		expandedHits := countHits(expanded, queryExpanded)

		bonus := float64(bigramHits)*0.5 + float64(stemHits)*0.3 + float64(expandedHits)*0.3

		bm25Score += bonus
		if bm25Score > maxBM25 {
			maxBM25 = bm25Score
		}

		positionBonus := 1.0 - 0.5*float64(i)/float64(len(scored))

		codeBoost := 0.0
		if containsCodeBlock(s) {
			codeBoost = 0.1
		}

		noisePenalty := 0.0
		if isNoiseSentence(s) {
			noisePenalty = 0.3
		}

		scored[i] = scoredSentence{
			Text:          s,
			Score:         bm25Score + 0.2*positionBonus + codeBoost - noisePenalty,
			OriginalIndex: i,
		}
	}

	sorted := make([]scoredSentence, len(scored))
	copy(sorted, scored)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].Score > sorted[j-1].Score; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}

	selected := make(map[int]bool)
	totalUsed := 0
	for _, s := range sorted {
		if s.Score <= 0 {
			continue
		}
		n := measure(s.Text, byToken)
		if totalUsed+n > budget {
			break
		}
		selected[s.OriginalIndex] = true
		totalUsed += n
	}

	if len(selected) == 0 {
		return truncateByBudget(sentences, budget, byToken)
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

// measure 按预算类型测量句子开销：token 估算或字符数。
func measure(s string, byToken bool) int {
	if byToken {
		return estimateTokens(s)
	}
	return len([]rune(s))
}

// truncateByBudget 顺序取句子直到预算用尽。
func truncateByBudget(sentences []string, budget int, byToken bool) string {
	var b strings.Builder
	used := 0
	for _, s := range sentences {
		n := measure(s, byToken)
		if used+n > budget {
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

func expandIdentifierTokens(tokens []string) []string {
	if len(tokens) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var result []string
	for _, t := range tokens {

		if strings.Contains(t, "_") {
			parts := strings.Split(t, "_")
			for _, p := range parts {
				p = strings.ToLower(p)
				if p != "" && !seen[p] {
					seen[p] = true
					result = append(result, p)
				}
			}
			continue
		}

		expanded := splitCamelCase(t)
		for _, p := range expanded {
			p = strings.ToLower(p)
			if p != "" && !seen[p] {
				seen[p] = true
				result = append(result, p)
			}
		}
	}
	return result
}

func splitCamelCase(word string) []string {
	if word == "" {
		return nil
	}
	var parts []string
	var buf strings.Builder
	runes := []rune(word)
	for i, r := range runes {
		if unicode.IsUpper(r) {
			if buf.Len() > 0 {

				if buf.Len() > 1 && i+1 < len(runes) && unicode.IsLower(runes[i+1]) {

					prev := buf.String()
					short := prev[:len(prev)-1]
					if short != "" {
						parts = append(parts, short)
					}
					buf.Reset()
					buf.WriteByte(byte(runes[i-1]))
				}
				parts = append(parts, buf.String())
				buf.Reset()
			}
			buf.WriteRune(r)
		} else {
			buf.WriteRune(r)
		}
	}
	if buf.Len() > 0 {
		parts = append(parts, buf.String())
	}
	return parts
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

// Tokenize 分词（供外部与内部共用）。
func Tokenize(text string) []string {
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

func isGarbledText(s string) bool {

	if strings.ContainsRune(s, '\uFFFD') {
		return true
	}

	runes := []rune(s)
	if len(runes) == 0 {
		return false
	}

	highBytes := 0
	cjkRunes := 0
	nonPrintable := 0

	for _, r := range runes {
		if r > 0x7F && r < 0xA0 {
			highBytes++
		}
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			cjkRunes++
		}
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			nonPrintable++
		}
	}

	if float64(highBytes)/float64(len(runes)) > 0.3 && cjkRunes == 0 {
		return true
	}

	if float64(nonPrintable)/float64(len(runes)) > 0.1 {
		return true
	}

	return false
}

func tryFixGarbled(s string) string {

	raw := make([]byte, 0, len(s))
	for _, r := range s {
		if r <= 0xFF {
			raw = append(raw, byte(r))
		} else {

			return ""
		}
	}

	if len(raw) == 0 {
		return ""
	}

	for _, d := range garbledDecoders {
		decoded, err := d.enc.NewDecoder().Bytes(raw)
		if err != nil {
			continue
		}
		result := string(decoded)

		if !isGarbledText(result) {
			return result
		}
	}

	return ""
}

func isNoiseSentence(s string) bool {
	low := strings.ToLower(s)
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
	_, ok := stopWords[w]
	return ok
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
