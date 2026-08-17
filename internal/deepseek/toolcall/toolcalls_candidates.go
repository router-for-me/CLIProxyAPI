package toolcall

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type canonicalToolMarkupAttr struct {
	Key   string
	Value string
}

func canonicalizeToolCallCandidateSpans(text string) string {
	if text == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); {
		next, advanced, blocked := skipXMLIgnoredSection(text, i)
		if blocked {
			b.WriteString(text[i:])
			break
		}
		if advanced {
			b.WriteString(text[i:next])
			i = next
			continue
		}
		if end, ok := markdownCodeSpanEnd(text, i); ok {
			b.WriteString(text[i:end])
			i = end
			continue
		}
		tag, ok := scanToolMarkupTagAt(text, i)
		if !ok {
			b.WriteByte(text[i])
			i++
			continue
		}
		b.WriteString(canonicalizeRecognizedToolMarkupTag(text[tag.Start:tag.End+1], tag))
		i = tag.End + 1
	}
	return b.String()
}

func canonicalizeRecognizedToolMarkupTag(raw string, tag ToolMarkupTag) string {
	if raw == "" {
		return raw
	}
	idx := 0
	if delimLen := xmlTagStartDelimiterLenAt(raw, idx); delimLen > 0 {
		idx += delimLen
	}
	for {
		idx = skipToolMarkupIgnorables(raw, idx)
		if delimLen := xmlTagStartDelimiterLenAt(raw, idx); delimLen > 0 {
			idx += delimLen
			continue
		}
		break
	}
	idx = skipToolMarkupIgnorables(raw, idx)
	if tag.Closing {
		if next, ok := consumeToolMarkupClosingSlash(raw, idx); ok {
			idx = next
		}
	}
	idx, _ = consumeToolMarkupNamePrefix(raw, idx)
	afterName, ok := consumeToolKeyword(raw, idx, rawNameForTag(tag))
	if !ok {
		afterName = idx
	}

	attrs := parseCanonicalToolMarkupAttrs(raw, afterName)

	var b strings.Builder
	b.Grow(len(raw) + 8)
	b.WriteByte('<')
	if tag.Closing {
		b.WriteByte('/')
	}
	if tag.EPSELike {
		b.WriteString("|EPSE|")
	}
	b.WriteString(tag.Name)
	for _, attr := range attrs {
		if attr.Key == "" {
			continue
		}
		b.WriteByte(' ')
		b.WriteString(attr.Key)
		b.WriteString(`="`)
		b.WriteString(quoteCanonicalXMLAttrValue(attr.Value))
		b.WriteByte('"')
	}
	if tag.SelfClosing {
		b.WriteByte('/')
	}
	b.WriteByte('>')
	return b.String()
}

func rawNameForTag(tag ToolMarkupTag) string {
	for _, name := range toolMarkupNames {
		if name.canonical == tag.Name {
			return name.raw
		}
	}
	return tag.Name
}

func parseCanonicalToolMarkupAttrs(raw string, idx int) []canonicalToolMarkupAttr {
	if raw == "" || idx >= len(raw) {
		return nil
	}
	var out []canonicalToolMarkupAttr
	for idx < len(raw) {
		idx = skipToolMarkupIgnorables(raw, idx)
		if idx >= len(raw) {
			break
		}
		if spacingLen := toolMarkupWhitespaceLikeLenAt(raw, idx); spacingLen > 0 {
			idx += spacingLen
			continue
		}
		if xmlTagEndDelimiterLenAt(raw, idx) > 0 {
			break
		}
		if next, ok := consumeToolMarkupPipe(raw, idx); ok {
			idx = next
			continue
		}
		if next, ok := consumeToolMarkupClosingSlash(raw, idx); ok {
			idx = next
			continue
		}

		keyStart := idx
		for idx < len(raw) {
			idx = skipToolMarkupIgnorables(raw, idx)
			if idx >= len(raw) {
				break
			}
			if spacingLen := toolMarkupWhitespaceLikeLenAt(raw, idx); spacingLen > 0 {
				break
			}
			if toolMarkupEqualsLenAt(raw, idx) > 0 || xmlTagEndDelimiterLenAt(raw, idx) > 0 {
				break
			}
			if _, ok := consumeToolMarkupPipe(raw, idx); ok {
				break
			}
			if _, ok := consumeToolMarkupClosingSlash(raw, idx); ok {
				break
			}
			_, size := utf8.DecodeRuneInString(raw[idx:])
			if size <= 0 {
				idx++
			} else {
				idx += size
			}
		}
		keyEnd := idx
		key := normalizeCanonicalToolAttrKey(raw[keyStart:keyEnd])
		idx = skipToolMarkupIgnorables(raw, idx)
		for {
			spacingLen := toolMarkupWhitespaceLikeLenAt(raw, idx)
			if spacingLen == 0 {
				break
			}
			idx += spacingLen
			idx = skipToolMarkupIgnorables(raw, idx)
		}
		if eqLen := toolMarkupEqualsLenAt(raw, idx); eqLen > 0 {
			idx += eqLen
		} else {
			continue
		}
		idx = skipToolMarkupIgnorables(raw, idx)
		for {
			spacingLen := toolMarkupWhitespaceLikeLenAt(raw, idx)
			if spacingLen == 0 {
				break
			}
			idx += spacingLen
			idx = skipToolMarkupIgnorables(raw, idx)
		}
		if key == "" {
			_, size := utf8.DecodeRuneInString(raw[idx:])
			if size <= 0 {
				idx++
			} else {
				idx += size
			}
			continue
		}

		value := ""
		if quote, quoteLen := xmlQuotePairAt(raw, idx); quoteLen > 0 {
			valueStart := idx + quoteLen
			idx = valueStart
			for idx < len(raw) {
				if closeLen := xmlQuoteCloseDelimiterLenAt(raw, idx, quote); closeLen > 0 {
					value = raw[valueStart:idx]
					idx += closeLen
					break
				}
				_, size := utf8.DecodeRuneInString(raw[idx:])
				if size <= 0 {
					idx++
				} else {
					idx += size
				}
			}
		} else {
			valueStart := idx
			for idx < len(raw) {
				if spacingLen := toolMarkupWhitespaceLikeLenAt(raw, idx); spacingLen > 0 {
					break
				}
				if xmlTagEndDelimiterLenAt(raw, idx) > 0 || toolMarkupEqualsLenAt(raw, idx) > 0 {
					break
				}
				if _, ok := consumeToolMarkupPipe(raw, idx); ok {
					break
				}
				if _, ok := consumeToolMarkupClosingSlash(raw, idx); ok {
					break
				}
				_, size := utf8.DecodeRuneInString(raw[idx:])
				if size <= 0 {
					idx++
				} else {
					idx += size
				}
			}
			value = raw[valueStart:idx]
		}

		out = append(out, canonicalToolMarkupAttr{
			Key:   key,
			Value: value,
		})
	}
	return out
}

func normalizeCanonicalToolAttrKey(raw string) string {
	trimmed := strings.TrimSpace(removeToolMarkupIgnorables(raw))
	if trimmed == "" {
		return ""
	}
	if next, ok := consumeToolKeyword(trimmed, 0, "name"); ok {
		if skipToolMarkupIgnorables(trimmed, next) == len(trimmed) {
			return "name"
		}
	}
	return ""
}

func quoteCanonicalXMLAttrValue(raw string) string {
	if raw == "" {
		return ""
	}
	return strings.ReplaceAll(raw, `"`, "&quot;")
}

func removeToolMarkupIgnorables(raw string) string {
	if raw == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(raw))
	for i := 0; i < len(raw); {
		if ignorableLen := toolMarkupIgnorableLenAt(raw, i); ignorableLen > 0 {
			i += ignorableLen
			continue
		}
		r, size := utf8.DecodeRuneInString(raw[i:])
		if size <= 0 {
			b.WriteByte(raw[i])
			i++
			continue
		}
		b.WriteRune(r)
		i += size
	}
	return b.String()
}

func skipToolMarkupIgnorables(text string, idx int) int {
	for idx < len(text) {
		if ignorableLen := toolMarkupIgnorableLenAt(text, idx); ignorableLen > 0 {
			idx += ignorableLen
			continue
		}
		break
	}
	return idx
}

func toolMarkupIgnorableLenAt(text string, idx int) int {
	if idx < 0 || idx >= len(text) {
		return 0
	}
	r, size := utf8.DecodeRuneInString(text[idx:])
	if size <= 0 {
		return 0
	}
	if unicode.Is(unicode.Cf, r) {
		return size
	}
	if unicode.IsControl(r) && !unicode.IsSpace(r) {
		return size
	}
	return 0
}

func toolMarkupEqualsLenAt(text string, idx int) int {
	idx = skipToolMarkupIgnorables(text, idx)
	if idx < 0 || idx >= len(text) {
		return 0
	}
	switch {
	case text[idx] == '=':
		return 1
	case strings.HasPrefix(text[idx:], "＝"):
		return len("＝")
	case strings.HasPrefix(text[idx:], "﹦"):
		return len("﹦")
	case strings.HasPrefix(text[idx:], "꞊"):
		return len("꞊")
	default:
		return 0
	}
}

func toolMarkupDashLenAt(text string, idx int) int {
	idx = skipToolMarkupIgnorables(text, idx)
	if idx < 0 || idx >= len(text) {
		return 0
	}
	switch {
	case text[idx] == '-':
		return 1
	case strings.HasPrefix(text[idx:], "‐"):
		return len("‐")
	case strings.HasPrefix(text[idx:], "‑"):
		return len("‑")
	case strings.HasPrefix(text[idx:], "‒"):
		return len("‒")
	case strings.HasPrefix(text[idx:], "–"):
		return len("–")
	case strings.HasPrefix(text[idx:], "—"):
		return len("—")
	case strings.HasPrefix(text[idx:], "―"):
		return len("―")
	case strings.HasPrefix(text[idx:], "−"):
		return len("−")
	case strings.HasPrefix(text[idx:], "﹣"):
		return len("﹣")
	case strings.HasPrefix(text[idx:], "－"):
		return len("－")
	default:
		return 0
	}
}

func toolMarkupUnderscoreLenAt(text string, idx int) int {
	idx = skipToolMarkupIgnorables(text, idx)
	if idx < 0 || idx >= len(text) {
		return 0
	}
	switch {
	case text[idx] == '_':
		return 1
	case strings.HasPrefix(text[idx:], "＿"):
		return len("＿")
	case strings.HasPrefix(text[idx:], "﹍"):
		return len("﹍")
	case strings.HasPrefix(text[idx:], "﹎"):
		return len("﹎")
	case strings.HasPrefix(text[idx:], "﹏"):
		return len("﹏")
	default:
		return 0
	}
}

func consumeToolKeyword(text string, idx int, keyword string) (int, bool) {
	next := idx
	for i := 0; i < len(keyword); i++ {
		next = skipToolMarkupIgnorables(text, next)
		if next >= len(text) {
			return idx, false
		}
		target := asciiLower(keyword[i])
		switch target {
		case '_':
			if underscoreLen := toolMarkupUnderscoreLenAt(text, next); underscoreLen > 0 {
				next += underscoreLen
				continue
			}
			return idx, false
		case '-':
			if dashLen := toolMarkupDashLenAt(text, next); dashLen > 0 {
				next += dashLen
				continue
			}
			return idx, false
		default:
			r, size := utf8.DecodeRuneInString(text[next:])
			if size <= 0 {
				return idx, false
			}
			folded, ok := foldToolKeywordRune(r)
			if !ok || folded != target {
				return idx, false
			}
			next += size
		}
	}
	return next, true
}

func foldToolKeywordRune(r rune) (byte, bool) {
	if r >= 'Ａ' && r <= 'Ｚ' {
		r = r - 'Ａ' + 'A'
	}
	if r >= 'ａ' && r <= 'ｚ' {
		r = r - 'ａ' + 'a'
	}
	r = unicode.ToLower(r)
	switch r {
	case 'a', 'c', 'd', 'e', 'i', 'k', 'l', 'm', 'n', 'o', 'p', 'r', 's', 't', 'v':
		return byte(r), true
	case 'а', 'Α', 'α':
		return 'a', true
	case 'с', 'С', 'ϲ', 'Ϲ':
		return 'c', true
	case 'ԁ', 'ⅾ':
		return 'd', true
	case 'е', 'Е', 'Ε', 'ε':
		return 'e', true
	case 'і', 'І', 'Ι', 'ι', 'ı':
		return 'i', true
	case 'к', 'К', 'Κ', 'κ':
		return 'k', true
	case 'ⅼ':
		return 'l', true
	case 'м', 'М', 'Μ', 'μ':
		return 'm', true
	case 'ո':
		return 'n', true
	case 'о', 'О', 'Ο', 'ο':
		return 'o', true
	case 'р', 'Р', 'Ρ', 'ρ':
		return 'p', true
	case 'ѕ', 'Ѕ':
		return 's', true
	case 'т', 'Т', 'Τ', 'τ':
		return 't', true
	case 'ν', 'Ν', 'ѵ', 'ⅴ':
		return 'v', true
	default:
		return 0, false
	}
}

func toolMarkupWhitespaceLikeLenAt(text string, idx int) int {
	idx = skipToolMarkupIgnorables(text, idx)
	if idx < 0 || idx >= len(text) {
		return 0
	}
	switch text[idx] {
	case ' ', '\t', '\n', '\r':
		return 1
	}
	if strings.HasPrefix(text[idx:], "▁") {
		return len("▁")
	}
	r, size := utf8.DecodeRuneInString(text[idx:])
	if size > 0 && unicode.IsSpace(r) {
		return size
	}
	return 0
}

func consumeToolMarkupPipe(text string, idx int) (int, bool) {
	idx = skipToolMarkupIgnorables(text, idx)
	if idx >= len(text) {
		return idx, false
	}
	switch {
	case text[idx] == '|':
		return idx + 1, true
	case strings.HasPrefix(text[idx:], "│"):
		return idx + len("│"), true
	case strings.HasPrefix(text[idx:], "∣"):
		return idx + len("∣"), true
	case strings.HasPrefix(text[idx:], "❘"):
		return idx + len("❘"), true
	case strings.HasPrefix(text[idx:], "ǀ"):
		return idx + len("ǀ"), true
	case strings.HasPrefix(text[idx:], "￨"):
		return idx + len("￨"), true
	default:
		return idx, false
	}
}

func consumeToolMarkupClosingSlash(text string, idx int) (int, bool) {
	idx = skipToolMarkupIgnorables(text, idx)
	if idx >= len(text) {
		return idx, false
	}
	switch {
	case text[idx] == '/':
		return idx + 1, true
	case strings.HasPrefix(text[idx:], "／"):
		return idx + len("／"), true
	case strings.HasPrefix(text[idx:], "∕"):
		return idx + len("∕"), true
	case strings.HasPrefix(text[idx:], "⁄"):
		return idx + len("⁄"), true
	case strings.HasPrefix(text[idx:], "⧸"):
		return idx + len("⧸"), true
	default:
		return idx, false
	}
}

func xmlTagStartDelimiterLenAt(text string, idx int) int {
	idx = skipToolMarkupIgnorables(text, idx)
	if idx < 0 || idx >= len(text) {
		return 0
	}
	switch {
	case text[idx] == '<':
		return 1
	case strings.HasPrefix(text[idx:], "＜"):
		return len("＜")
	case strings.HasPrefix(text[idx:], "﹤"):
		return len("﹤")
	case strings.HasPrefix(text[idx:], "〈"):
		return len("〈")
	default:
		return 0
	}
}

func xmlTagEndDelimiterLenAt(text string, idx int) int {
	idx = skipToolMarkupIgnorables(text, idx)
	if idx < 0 || idx >= len(text) {
		return 0
	}
	switch {
	case text[idx] == '>':
		return 1
	case strings.HasPrefix(text[idx:], "＞"):
		return len("＞")
	case strings.HasPrefix(text[idx:], "﹥"):
		return len("﹥")
	case strings.HasPrefix(text[idx:], "〉"):
		return len("〉")
	default:
		return 0
	}
}

func xmlTagEndDelimiterLenEndingAt(text string, end int) int {
	if end < 0 || end >= len(text) {
		return 0
	}
	if text[end] == '>' {
		return 1
	}
	if end+1 >= len("＞") && text[end+1-len("＞"):end+1] == "＞" {
		return len("＞")
	}
	return 0
}

func xmlQuotePairAt(text string, idx int) (string, int) {
	idx = skipToolMarkupIgnorables(text, idx)
	if idx < 0 || idx >= len(text) {
		return "", 0
	}
	switch {
	case text[idx] == '"':
		return `"`, 1
	case text[idx] == '\'':
		return `'`, 1
	case strings.HasPrefix(text[idx:], "“"):
		return "”", len("“")
	case strings.HasPrefix(text[idx:], "‘"):
		return "’", len("‘")
	case strings.HasPrefix(text[idx:], "＂"):
		return "＂", len("＂")
	case strings.HasPrefix(text[idx:], "＇"):
		return "＇", len("＇")
	case strings.HasPrefix(text[idx:], "„"):
		return "”", len("„")
	case strings.HasPrefix(text[idx:], "‟"):
		return "”", len("‟")
	default:
		return "", 0
	}
}

func xmlQuoteCloseDelimiterLenAt(text string, idx int, quote string) int {
	if quote == "" || idx < 0 || idx >= len(text) {
		return 0
	}
	if strings.HasPrefix(text[idx:], quote) {
		return len(quote)
	}
	return 0
}

func hasRepairableXMLToolCallsWrapper(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	if _, ok := firstToolMarkupTagByName(text, "tool_calls", false); ok {
		return false
	}
	invokeTag, ok := firstToolMarkupTagByName(text, "invoke", false)
	if !ok {
		return false
	}
	closeTag, ok := lastToolMarkupTagByName(text, "tool_calls", true)
	if !ok {
		return false
	}
	return invokeTag.Start < closeTag.Start
}

func toolCDATAOpenLenAt(text string, idx int) int {
	start := skipToolMarkupIgnorables(text, idx)
	ltLen := xmlTagStartDelimiterLenAt(text, start)
	if ltLen == 0 {
		return 0
	}
	pos := start + ltLen
	for skipped := 0; skipped <= 4 && pos < len(text); skipped++ {
		pos = skipToolMarkupIgnorables(text, pos)
		if pos >= len(text) {
			return 0
		}
		if text[pos] == '[' {
			pos++
			next, ok := consumeToolKeyword(text, pos, "cdata")
			if !ok {
				return 0
			}
			pos = skipToolMarkupIgnorables(text, next)
			if pos >= len(text) || text[pos] != '[' {
				return 0
			}
			pos++
			return pos - idx
		}
		r, size := utf8.DecodeRuneInString(text[pos:])
		if size <= 0 || !isToolMarkupSeparator(r) {
			return 0
		}
		pos += size
	}
	return 0
}

func indexToolCDATAOpen(text string, start int) int {
	for i := maxInt(start, 0); i < len(text); i++ {
		if toolCDATAOpenLenAt(text, i) > 0 {
			return i
		}
	}
	return -1
}

func findTrailingToolCDATACloseStart(text string) int {
	for i := len(text) - 1; i >= 0; i-- {
		if closeLen := toolCDATACloseLenAt(text, i); closeLen > 0 && i+closeLen == len(text) {
			return i
		}
	}
	return -1
}
