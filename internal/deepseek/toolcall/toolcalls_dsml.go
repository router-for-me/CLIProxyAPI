package toolcall

import (
	"strings"
)

func normalizeEPSEToolCallMarkup(text string) (string, bool) {
	if text == "" {
		return "", true
	}
	canonicalized := canonicalizeToolCallCandidateSpans(text)
	hasEPSELikeMarkup, hasCanonicalMarkup := ContainsToolMarkupSyntaxOutsideIgnored(canonicalized)
	if !hasEPSELikeMarkup && !hasCanonicalMarkup {
		return canonicalized, true
	}
	return rewriteEPSEToolMarkupOutsideIgnored(canonicalized), true
}

func rewriteEPSEToolMarkupOutsideIgnored(text string) string {
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
		b.WriteByte('<')
		if tag.Closing {
			b.WriteByte('/')
		}
		b.WriteString(tag.Name)
		if delimLen := xmlTagEndDelimiterLenEndingAt(text, tag.End); delimLen > 0 {
			b.WriteString(text[tag.NameEnd : tag.End+1-delimLen])
			b.WriteByte('>')
		} else {
			b.WriteString(text[tag.NameEnd : tag.End+1])
			b.WriteByte('>')
		}
		i = tag.End + 1
	}
	return b.String()
}
