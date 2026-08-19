package toolstream

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/deepseek/toolcall"
	"strings"
)

// consumeXMLToolCapture tries to extract complete XML tool call blocks from captured text.
func consumeXMLToolCapture(captured string, toolNames []string) (prefix string, calls []toolcall.ParsedToolCall, suffix string, ready bool) {
	anyOpenFound := false
	type candidate struct {
		start  int
		prefix string
		calls  []toolcall.ParsedToolCall
		suffix string
	}
	type rejectedBlock struct {
		start  int
		prefix string
		suffix string
	}
	var best *candidate
	var rejected *rejectedBlock

	// Scan every recognized tool tag occurrence. Prose can mention a wrapper
	// tag before the actual tool block, including the same variant as the real
	// block. We only accept complete tool_calls wrappers that parse cleanly.
	for searchFrom := 0; searchFrom < len(captured); {
		tag, ok := toolcall.FindToolMarkupTagOutsideIgnored(captured, searchFrom)
		if !ok {
			break
		}
		if tag.Closing || tag.Name != "tool_calls" {
			searchFrom = tag.End + 1
			continue
		}
		closeTag, ok := toolcall.FindMatchingToolMarkupClose(captured, tag)
		if !ok {
			anyOpenFound = true
			searchFrom = tag.End + 1
			continue
		}

		xmlBlock := captured[tag.Start : closeTag.End+1]
		prefixPart := captured[:tag.Start]
		suffixPart := captured[closeTag.End+1:]
		parsed := toolcall.ParseStandaloneToolCallsDetailed(xmlBlock, toolNames)
		if len(parsed.Calls) > 0 {
			prefixPart, suffixPart = trimWrappingJSONFence(prefixPart, suffixPart)
			if best == nil || tag.Start < best.start {
				best = &candidate{start: tag.Start, prefix: prefixPart, calls: parsed.Calls, suffix: suffixPart}
			}
			break
		}
		if parsed.SawToolCallSyntax {
			if rejected == nil || tag.Start < rejected.start {
				rejected = &rejectedBlock{start: tag.Start, prefix: prefixPart + xmlBlock, suffix: suffixPart}
			}
			searchFrom = tag.End + 1
			continue
		}
		if rejected == nil || tag.Start < rejected.start {
			rejected = &rejectedBlock{start: tag.Start, prefix: prefixPart + xmlBlock, suffix: suffixPart}
		}
		searchFrom = tag.End + 1
	}
	if best != nil {
		return best.prefix, best.calls, best.suffix, true
	}
	if anyOpenFound {
		// At least one opening tag was found but none had a matching close tag.
		// Keep buffering until a closing tag arrives.
		return "", nil, "", false
	}
	if rejected != nil {
		// If this block failed to become a tool call, pass it through as text.
		return rejected.prefix, nil, rejected.suffix, true
	}
	if invokeTag, ok := findFirstToolMarkupTagByName(captured, 0, "invoke"); ok {
		if wrapperOpen, ok := findFirstToolMarkupTagByName(captured, 0, "tool_calls"); !ok || wrapperOpen.Start > invokeTag.Start {
			if closeTag, ok := findFirstToolMarkupTagByNameFrom(captured, invokeTag.Start+1, "tool_calls", true); ok && closeTag.Start > invokeTag.Start {
				xmlBlock := "<tool_calls>" + captured[invokeTag.Start:closeTag.End+1]
				prefixPart := captured[:invokeTag.Start]
				suffixPart := captured[closeTag.End+1:]
				parsed := toolcall.ParseStandaloneToolCallsDetailed(xmlBlock, toolNames)
				if len(parsed.Calls) > 0 {
					prefixPart, suffixPart = trimWrappingJSONFence(prefixPart, suffixPart)
					return prefixPart, parsed.Calls, suffixPart, true
				}
				if parsed.SawToolCallSyntax {
					return prefixPart + captured[invokeTag.Start:closeTag.End+1], nil, suffixPart, true
				}
				return prefixPart + captured[invokeTag.Start:closeTag.End+1], nil, suffixPart, true
			}
		}
	}
	return "", nil, "", false
}

// hasOpenXMLToolTag returns true if captured text contains an XML tool opening tag
// whose SPECIFIC closing tag has not appeared yet.
func hasOpenXMLToolTag(captured string) bool {
	for searchFrom := 0; searchFrom < len(captured); {
		tag, ok := toolcall.FindToolMarkupTagOutsideIgnored(captured, searchFrom)
		if !ok {
			return false
		}
		if tag.Closing || tag.Name != "tool_calls" {
			searchFrom = tag.End + 1
			continue
		}
		if _, ok := toolcall.FindMatchingToolMarkupClose(captured, tag); !ok {
			return true
		}
		searchFrom = tag.End + 1
	}
	return false
}

func shouldKeepBareInvokeCapture(captured string) bool {
	invokeTag, ok := findFirstToolMarkupTagByName(captured, 0, "invoke")
	if !ok {
		return false
	}
	if wrapperOpen, ok := findFirstToolMarkupTagByName(captured, 0, "tool_calls"); ok && wrapperOpen.Start <= invokeTag.Start {
		return false
	}
	if closeTag, ok := findFirstToolMarkupTagByNameFrom(captured, invokeTag.Start+1, "tool_calls", true); ok && closeTag.Start > invokeTag.Start {
		return true
	}
	startEnd := invokeTag.End
	if startEnd < 0 {
		return true
	}
	body := captured[startEnd+1:]
	trimmedBody := strings.TrimLeft(body, " \t\r\n")
	if trimmedBody == "" {
		return true
	}

	if invokeCloseTag, ok := findFirstToolMarkupTagByNameFrom(captured, startEnd+1, "invoke", true); ok {
		return strings.TrimSpace(captured[invokeCloseTag.End+1:]) == ""
	}
	if paramTag, ok := findFirstToolMarkupTagByName(body, 0, "parameter"); ok && strings.TrimSpace(body[:paramTag.Start]) == "" {
		return true
	}

	trimmedLower := strings.ToLower(trimmedBody)
	return strings.HasPrefix(trimmedLower, "<parameter") ||
		strings.HasPrefix(trimmedLower, "{") ||
		strings.HasPrefix(trimmedLower, "[")
}

func findPartialXMLToolTagStart(s string) int {
	lastLT := lastToolMarkupStartDelimiterIndex(s)
	if lastLT < 0 {
		return -1
	}
	start := includeDuplicateLeadingLessThan(s, lastLT)
	tail := s[start:]
	// If there's a tag terminator in the tail, the tag is closed — not partial.
	if strings.Contains(tail, ">") || strings.Contains(tail, "＞") {
		return -1
	}
	if toolcall.IsPartialToolMarkupTagPrefix(tail) {
		return start
	}
	return -1
}

func lastToolMarkupStartDelimiterIndex(s string) int {
	asciiIdx := strings.LastIndex(s, "<")
	fullwidthIdx := strings.LastIndex(s, "＜")
	if asciiIdx > fullwidthIdx {
		return asciiIdx
	}
	return fullwidthIdx
}
