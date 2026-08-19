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
	// Stack of open tool-markup tag names. Used to recover anonymous EPSE
	// closes (</|EPSE> or </|EPSE|>) by mapping them to the tag that's
	// actually being closed.
	var stack []string
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
		// Anonymous EPSE close: </|EPSE> or </|EPSE|>. Models truncate the
		// root tool_calls close (and sometimes invoke/parameter closes) this
		// way. Recover the intended name from the open-tag stack.
		if anonTag, ok := matchAnonymousEPSECloseAt(text, i); ok {
			name := "tool_calls"
			if n := len(stack); n > 0 {
				name = stack[n-1]
				stack = stack[:n-1]
			}
			b.WriteString("</")
			b.WriteString(name)
			b.WriteByte('>')
			i = anonTag.End + 1
			continue
		}
		tag, ok := scanToolMarkupTagAt(text, i)
		if !ok {
			b.WriteByte(text[i])
			i++
			continue
		}
		// Track open/close for stack accuracy.
		if tag.Closing {
			for j := len(stack) - 1; j >= 0; j-- {
				if stack[j] == tag.Name {
					stack = stack[:j]
					break
				}
			}
		} else if !tag.SelfClosing {
			stack = append(stack, tag.Name)
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
