package pkg

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

// BM25 参数
const (
	bm25K1 = 1.5  // 词频饱和参数
	bm25B  = 0.75 // 长度归一化参数
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

	// 先尝试修复乱码句子，修复不了才滤除
	repaired := make([]string, 0, len(sentences))
	for _, s := range sentences {
		if isGarbledText(s) {
			if fixed := tryFixGarbled(s); fixed != "" {
				repaired = append(repaired, fixed)
			}
			// 修复不了则丢弃
		} else {
			repaired = append(repaired, s)
		}
	}
	sentences = repaired

	if len(sentences) <= 2 {
		return truncateByTokens(sentences, maxTokens)
	}

	queryTokens := tokenize(query)
	if len(queryTokens) == 0 {
		return truncateByTokens(sentences, maxTokens)
	}

	queryStems := stemTokens(queryTokens)
	queryBigrams := buildBigrams(queryTokens)
	queryExpanded := expandIdentifierTokens(queryTokens)

	isCodeQ := isCodeQuery(query)
	isSymbolQ := isSymbolQuery(query)
	_ = isCodeQ
	_ = isSymbolQ

	// 统计全局 IDF 所需的文档级统计量
	type docStats struct {
		length int
		tf     map[string]int // term frequency in this sentence
	}
	sentenceStats := make([]docStats, len(sentences))
	totalDocs := len(sentences)
	docFreq := make(map[string]int) // 包含该 term 的句子数
	for i, s := range sentences {
		tokens := tokenize(s)
		stats := docStats{length: len(tokens), tf: make(map[string]int)}
		seen := make(map[string]bool)
		for _, t := range tokens {
			stats.tf[t]++
			if !seen[t] {
				seen[t] = true
				docFreq[t]++
			}
		}
		// 也计入展开后的标识符
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
	if avgDocLen < 1 {
		avgDocLen = 1
	}

	// BM25 评分 + 重排序信号
	scored := make([]scoredSentence, len(sentences))
	maxBM25 := 0.0
	for i, s := range sentences {
		tokens := tokenize(s)
		expanded := expandIdentifierTokens(tokens)

		// BM25 核心评分
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
			docLen := sentenceStats[i].length
			if docLen < 1 {
				docLen = 1
			}
			bm25Score += idf * (float64(tf) * (bm25K1 + 1)) /
				(float64(tf) + bm25K1*(1-bm25B+bm25B*float64(docLen)/float64(avgDocLen)))
		}

		// 额外信号：bigram 匹配、词干匹配、展开标识符匹配
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

// isSymbolQuery 检测查询是否为符号/标识符查询（如 Foo::bar, getUserById）
func isSymbolQuery(query string) bool {
	q := strings.ToLower(query)
	parts := strings.Fields(q)
	if len(parts) == 0 {
		return false
	}
	symbolCount := 0
	for _, p := range parts {
		// 包含特殊符号
		if strings.ContainsAny(p, "::->._") {
			symbolCount++
			continue
		}
		// camelCase 检测
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
			symbolCount++
			continue
		}
	}
	// 过半 token 是符号形式
	return len(parts) > 0 && float64(symbolCount)/float64(len(parts)) >= 0.5
}

// expandIdentifierTokens 展开 camelCase/snake_case 标识符为子 token
// 例如: "parseConfig" → ["parse", "config"], "config_parser" → ["config", "parser"]
func expandIdentifierTokens(tokens []string) []string {
	if len(tokens) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var result []string
	for _, t := range tokens {
		// snake_case
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
		// camelCase / PascalCase
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

// splitCamelCase 将 camelCase/PascalCase 拆分为子词
// "parseConfig" → ["parse", "Config"], "XMLParser" → ["XML", "Parser"]
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
				// 处理连续大写缩写: "XMLParser" → "XML" + "Parser"
				if buf.Len() > 1 && i+1 < len(runes) && unicode.IsLower(runes[i+1]) {
					// 前一个字符是大写，当前也是大写，下一个是小写
					// 说明前面是缩写，当前是新的词开始
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

func isGarbledText(s string) bool {
	// 检测 U+FFFD replacement character — 明确表示编码错误
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
			highBytes++ // ISO-8859-1 控制字符区域
		}
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			cjkRunes++
		}
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			nonPrintable++
		}
	}

	// 高位字节占比 > 30% 且无 CJK → 很可能是 Latin-1 被误解析为 UTF-8
	if float64(highBytes)/float64(len(runes)) > 0.3 && cjkRunes == 0 {
		return true
	}

	// 不可打印字符占比 > 10%
	if float64(nonPrintable)/float64(len(runes)) > 0.1 {
		return true
	}

	return false
}

// tryFixGarbled 尝试用常见编码重新解码乱码文本。
// 将当前文本视为 Latin-1 编码的字节序列，尝试用其他编码重新解释。
// 返回修复后的文本，若无法修复则返回空字符串。
func tryFixGarbled(s string) string {
	// 将当前 UTF-8 字符串转回原始字节（假设它是被误解析的 Latin-1）
	raw := make([]byte, 0, len(s))
	for _, r := range s {
		if r <= 0xFF {
			raw = append(raw, byte(r))
		} else {
			// 包含有效多字节 UTF-8 字符，不是单纯的编码错误
			return ""
		}
	}

	if len(raw) == 0 {
		return ""
	}

	// 按优先级尝试各编码解码
	decoders := []struct {
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

	for _, d := range decoders {
		decoded, err := d.enc.NewDecoder().Bytes(raw)
		if err != nil {
			continue
		}
		result := string(decoded)
		// 解码后不再乱码才算修复成功
		if !isGarbledText(result) {
			return result
		}
	}

	return ""
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
