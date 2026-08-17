package toolcall

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type toolMarkupNameAlias struct {
	raw       string
	canonical string
	epseOnly  bool
}

var toolMarkupNames = []toolMarkupNameAlias{
	{raw: "tool_calls", canonical: "tool_calls"},
	{raw: "tool-calls", canonical: "tool_calls", epseOnly: true},
	{raw: "toolcalls", canonical: "tool_calls", epseOnly: true},
	{raw: "invoke", canonical: "invoke"},
	{raw: "parameter", canonical: "parameter"},
}

type ToolMarkupTag struct {
	Start       int
	End         int
	NameStart   int
	NameEnd     int
	Name        string
	Closing     bool
	SelfClosing bool
	EPSELike    bool
	Canonical   bool
}

func ContainsToolMarkupSyntaxOutsideIgnored(text string) (hasEPSE, hasCanonical bool) {
	for i := 0; i < len(text); {
		next, advanced, blocked := skipXMLIgnoredSection(text, i)
		if blocked {
			return hasEPSE, hasCanonical
		}
		if advanced {
			i = next
			continue
		}
		if end, ok := markdownCodeSpanEnd(text, i); ok {
			i = end
			continue
		}
		if tag, ok := scanToolMarkupTagAt(text, i); ok {
			if tag.EPSELike {
				hasEPSE = true
			} else {
				hasCanonical = true
			}
			if hasEPSE && hasCanonical {
				return true, true
			}
			i = tag.End + 1
			continue
		}
		i++
	}
	return hasEPSE, hasCanonical
}

func ContainsToolCallWrapperSyntaxOutsideIgnored(text string) (hasEPSE, hasCanonical bool) {
	for i := 0; i < len(text); {
		next, advanced, blocked := skipXMLIgnoredSection(text, i)
		if blocked {
			return hasEPSE, hasCanonical
		}
		if advanced {
			i = next
			continue
		}
		if end, ok := markdownCodeSpanEnd(text, i); ok {
			i = end
			continue
		}
		if tag, ok := scanToolMarkupTagAt(text, i); ok {
			if tag.Name != "tool_calls" {
				i = tag.End + 1
				continue
			}
			if tag.EPSELike {
				hasEPSE = true
			} else {
				hasCanonical = true
			}
			if hasEPSE && hasCanonical {
				return true, true
			}
			i = tag.End + 1
			continue
		}
		i++
	}
	return hasEPSE, hasCanonical
}

func FindToolMarkupTagOutsideIgnored(text string, start int) (ToolMarkupTag, bool) {
	for i := maxInt(start, 0); i < len(text); {
		next, advanced, blocked := skipXMLIgnoredSection(text, i)
		if blocked {
			return ToolMarkupTag{}, false
		}
		if advanced {
			i = next
			continue
		}
		if end, ok := markdownCodeSpanEnd(text, i); ok {
			i = end
			continue
		}
		if tag, ok := scanToolMarkupTagAt(text, i); ok {
			return tag, true
		}
		i++
	}
	return ToolMarkupTag{}, false
}

func FindMatchingToolMarkupClose(text string, open ToolMarkupTag) (ToolMarkupTag, bool) {
	if text == "" || open.Name == "" || open.Closing || open.End >= len(text) {
		return ToolMarkupTag{}, false
	}
	depth := 1
	for pos := open.End + 1; pos < len(text); {
		tag, ok := FindToolMarkupTagOutsideIgnored(text, pos)
		if !ok {
			return ToolMarkupTag{}, false
		}
		if tag.Name != open.Name {
			pos = tag.End + 1
			continue
		}
		if tag.Closing {
			depth--
			if depth == 0 {
				return tag, true
			}
		} else if !tag.SelfClosing {
			depth++
		}
		pos = tag.End + 1
	}
	return ToolMarkupTag{}, false
}

func scanToolMarkupTagAt(text string, start int) (ToolMarkupTag, bool) {
	next, ok := consumeToolMarkupLessThan(text, start)
	if !ok {
		return ToolMarkupTag{}, false
	}
	i := next
	for {
		next, ok := consumeToolMarkupLessThan(text, i)
		if !ok {
			break
		}
		i = next
	}
	closing := false
	if next, ok := consumeToolMarkupClosingSlash(text, i); ok {
		closing = true
		i = next
	}
	prefixStart := i
	i, epseLike := consumeToolMarkupNamePrefix(text, i)
	name, nameLen := matchToolMarkupName(text, i, epseLike)
	if nameLen == 0 {
		fallbackName, fallbackStart, fallbackLen, ok := matchToolMarkupNameAfterArbitraryPrefix(text, prefixStart)
		if !ok {
			return ToolMarkupTag{}, false
		}
		if !closing && toolMarkupPrefixContainsSlash(text[prefixStart:fallbackStart]) {
			closing = true
		}
		name = fallbackName
		i = fallbackStart
		nameLen = fallbackLen
		epseLike = true
	}
	nameEnd := i + nameLen
	nameEndBeforeSeparators := nameEnd
	for next, ok := consumeToolMarkupSeparator(text, nameEnd); ok; next, ok = consumeToolMarkupSeparator(text, nameEnd) {
		nameEnd = next
	}
	hasTrailingSeparator := nameEnd > nameEndBeforeSeparators
	if !hasToolMarkupBoundary(text, nameEnd) {
		return ToolMarkupTag{}, false
	}
	end := findXMLTagEnd(text, nameEnd)
	if end < 0 {
		if !hasTrailingSeparator {
			return ToolMarkupTag{}, false
		}
		end = nameEnd - 1
	}
	if hasTrailingSeparator {
		if nextLT := strings.IndexByte(text[nameEnd:], '<'); nextLT >= 0 && end >= nameEnd+nextLT {
			end = nameEnd - 1
		}
	}
	trimmed := strings.TrimSpace(text[start : end+1])
	return ToolMarkupTag{
		Start:       start,
		End:         end,
		NameStart:   i,
		NameEnd:     nameEnd,
		Name:        name,
		Closing:     closing,
		SelfClosing: strings.HasSuffix(trimmed, "/>"),
		EPSELike:    epseLike,
		Canonical:   !epseLike,
	}, true
}

func IsPartialToolMarkupTagPrefix(text string) bool {
	if text == "" || text[0] != '<' || strings.Contains(text, ">") || strings.Contains(text, "＞") {
		return false
	}
	i := 1
	for i < len(text) && text[i] == '<' {
		i++
	}
	if i >= len(text) {
		return true
	}
	if text[i] == '/' {
		i++
	}
	for i <= len(text) {
		if i == len(text) {
			return true
		}
		if hasToolMarkupNamePrefix(text, i) {
			return true
		}
		if hasASCIIPartialPrefixFoldAt(text, i, "epse") {
			return true
		}
		if hasPartialToolMarkupNameAfterArbitraryPrefix(text, i) {
			return true
		}
		next, ok := consumeToolMarkupNamePrefixOnce(text, i)
		if !ok {
			return false
		}
		i = next
	}
	return false
}

func consumeToolMarkupNamePrefix(text string, idx int) (int, bool) {
	epseLike := false
	for {
		next, ok := consumeToolMarkupNamePrefixOnce(text, idx)
		if !ok {
			return idx, epseLike
		}
		idx = next
		epseLike = true
	}
}

func consumeToolMarkupNamePrefixOnce(text string, idx int) (int, bool) {
	idx = skipToolMarkupIgnorables(text, idx)
	if next, ok := consumeToolMarkupSeparator(text, idx); ok {
		return next, true
	}
	if spacingLen := toolMarkupWhitespaceLikeLenAt(text, idx); spacingLen > 0 {
		return idx + spacingLen, true
	}
	if next, ok := consumeToolKeyword(text, idx, "epse"); ok {
		if dashLen := toolMarkupDashLenAt(text, next); dashLen > 0 {
			next += dashLen
		} else if underscoreLen := toolMarkupUnderscoreLenAt(text, next); underscoreLen > 0 {
			next += underscoreLen
		}
		return next, true
	}
	if next, ok := consumeArbitraryToolMarkupNamePrefix(text, idx); ok {
		return next, true
	}
	return idx, false
}

func consumeArbitraryToolMarkupNamePrefix(text string, idx int) (int, bool) {
	nextSegment, ok := consumeToolMarkupPrefixSegment(text, idx)
	if !ok {
		return idx, false
	}
	j := nextSegment
	for {
		nextSegment, ok = consumeToolMarkupPrefixSegment(text, j)
		if !ok {
			break
		}
		j = nextSegment
	}
	k := j
	for k < len(text) && (text[k] == ' ' || text[k] == '\t' || text[k] == '\r' || text[k] == '\n') {
		k++
	}
	next, ok := consumeToolMarkupSeparator(text, k)
	if !ok {
		if sep, size := normalizedASCIIAt(text, k); sep == '_' || sep == '-' {
			next = k + size
			ok = true
		}
	}
	if !ok {
		return idx, false
	}
	for next < len(text) && (text[next] == ' ' || text[next] == '\t' || text[next] == '\r' || text[next] == '\n') {
		next++
	}
	if !hasToolMarkupNamePrefix(text, next) {
		return idx, false
	}
	return next, true
}

func consumeToolMarkupPrefixSegment(text string, idx int) (int, bool) {
	ch, size := normalizedASCIIAt(text, idx)
	if size <= 0 {
		return idx, false
	}
	if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') {
		return idx + size, true
	}
	return idx, false
}

func hasASCIIPartialPrefixFoldAt(text string, start int, prefix string) bool {
	if start < 0 || start >= len(text) {
		return false
	}
	idx := start
	matched := 0
	for matched < len(prefix) && idx < len(text) {
		ch, size := normalizedASCIIAt(text, idx)
		if size <= 0 || asciiLower(ch) != asciiLower(prefix[matched]) {
			return false
		}
		idx += size
		matched++
	}
	return matched > 0 && matched < len(prefix) && idx == len(text)
}

func hasToolMarkupNamePrefix(text string, start int) bool {
	for _, name := range toolMarkupNames {
		if hasASCIIPrefixFoldAt(text, start, name.raw) {
			return true
		}
		if hasASCIIPartialPrefixFoldAt(text, start, name.raw) {
			return true
		}
	}
	return false
}

func matchToolMarkupName(text string, start int, epseLike bool) (string, int) {
	for _, name := range toolMarkupNames {
		if name.epseOnly && !epseLike {
			continue
		}
		if next, ok := consumeToolKeyword(text, start, name.raw); ok {
			return name.canonical, next - start
		}
	}
	return "", 0
}

func matchToolMarkupNameAfterArbitraryPrefix(text string, start int) (string, int, int, bool) {
	for idx := start; idx < len(text); {
		if isToolMarkupTagTerminator(text, idx) {
			return "", 0, 0, false
		}
		for _, name := range toolMarkupNames {
			next, ok := consumeToolKeyword(text, idx, name.raw)
			if !ok {
				continue
			}
			if !toolMarkupPrefixAllowsLocalNameAt(text, start, idx) {
				continue
			}
			return name.canonical, idx, next - idx, true
		}
		_, size := utf8.DecodeRuneInString(text[idx:])
		if size <= 0 {
			size = 1
		}
		idx += size
	}
	return "", 0, 0, false
}

func hasPartialToolMarkupNameAfterArbitraryPrefix(text string, start int) bool {
	for idx := start; idx < len(text); {
		if isToolMarkupTagTerminator(text, idx) {
			return false
		}
		if toolMarkupPrefixAllowsLocalNameAt(text, start, idx) && hasToolMarkupNamePrefix(text, idx) {
			return true
		}
		if toolMarkupPrefixAllowsLocalNameAt(text, start, idx) && hasEPSENamePrefixOrPartial(text, idx) {
			return true
		}
		_, size := utf8.DecodeRuneInString(text[idx:])
		if size <= 0 {
			size = 1
		}
		idx += size
	}
	return toolMarkupPrefixAllowsLocalName(text[start:])
}

func toolMarkupPrefixAllowsLocalNameAt(text string, start, localStart int) bool {
	if start < 0 || localStart <= start || localStart > len(text) {
		return false
	}
	prefix := text[start:localStart]
	if toolMarkupPrefixAllowsLocalName(prefix) {
		return true
	}
	if strings.ContainsAny(prefix, "=\"'") {
		return false
	}
	prev, prevSize := utf8.DecodeLastRuneInString(prefix)
	next, _ := utf8.DecodeRuneInString(text[localStart:])
	if prevSize <= 0 || next == utf8.RuneError {
		return false
	}
	return isASCIIAlphaNumeric(normalizeFullwidthASCII(prev)) && isASCIIUpper(normalizeFullwidthASCII(next))
}

func hasEPSENamePrefixOrPartial(text string, start int) bool {
	return hasASCIIPrefixFoldAt(text, start, "epse") || hasASCIIPartialPrefixFoldAt(text, start, "epse")
}

func toolMarkupPrefixAllowsLocalName(prefix string) bool {
	if prefix == "" {
		return false
	}
	if strings.Contains(normalizedASCIILowerString(prefix), "epse") {
		return true
	}
	if strings.ContainsAny(prefix, "=\"'") {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(prefix)
	r = normalizeFullwidthASCII(r)
	return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9')
}

func normalizedASCIILowerString(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		r = normalizeFullwidthASCII(r)
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		if r <= 0x7f {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isASCIIAlphaNumeric(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func isASCIIUpper(r rune) bool {
	return r >= 'A' && r <= 'Z'
}

func isToolMarkupTagTerminator(text string, idx int) bool {
	if idx >= len(text) {
		return false
	}
	if text[idx] == '>' {
		return true
	}
	r, _ := utf8.DecodeRuneInString(text[idx:])
	return normalizeFullwidthASCII(r) == '>'
}

func consumeToolMarkupSeparator(text string, idx int) (int, bool) {
	idx = skipToolMarkupIgnorables(text, idx)
	if idx >= len(text) {
		return idx, false
	}
	r, size := utf8.DecodeRuneInString(text[idx:])
	if size <= 0 || !isToolMarkupSeparator(r) {
		return idx, false
	}
	return idx + size, true
}

func isToolMarkupSeparator(r rune) bool {
	ch := normalizeFullwidthASCII(r)
	if ch == 0 || ch == '<' || ch == '>' || ch == '/' || ch == '=' || ch == '"' || ch == '\'' || ch == '[' {
		return false
	}
	if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' {
		return false
	}
	if r == '▁' || unicode.IsSpace(r) {
		return false
	}
	if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') {
		return false
	}
	return true
}

func consumeToolMarkupLessThan(text string, idx int) (int, bool) {
	idx = skipToolMarkupIgnorables(text, idx)
	ch, size := normalizedASCIIAt(text, idx)
	if size <= 0 || ch != '<' {
		return idx, false
	}
	return idx + size, true
}

func hasToolMarkupBoundary(text string, idx int) bool {
	idx = skipToolMarkupIgnorables(text, idx)
	if idx >= len(text) {
		return true
	}
	if toolMarkupWhitespaceLikeLenAt(text, idx) > 0 {
		return true
	}
	if _, ok := consumeToolMarkupClosingSlash(text, idx); ok {
		return true
	}
	return xmlTagEndDelimiterLenAt(text, idx) > 0
}

func normalizedASCIIAt(text string, idx int) (byte, int) {
	if idx < 0 || idx >= len(text) {
		return 0, 0
	}
	r, size := utf8.DecodeRuneInString(text[idx:])
	if r == utf8.RuneError && size == 0 {
		return 0, 0
	}
	normalized := normalizeFullwidthASCII(r)
	if normalized > 0x7f {
		return 0, 0
	}
	return byte(normalized), size
}

func normalizeFullwidthASCII(r rune) rune {
	switch r {
	case '〈':
		return '<'
	case '〉':
		return '>'
	case '“', '”':
		return '"'
	case '‘', '’':
		return '\''
	}
	if r >= '！' && r <= '～' {
		return r - 0xFEE0
	}
	return r
}

func toolMarkupPrefixContainsSlash(prefix string) bool {
	for _, r := range prefix {
		if normalizeFullwidthASCII(r) == '/' {
			return true
		}
	}
	return false
}
