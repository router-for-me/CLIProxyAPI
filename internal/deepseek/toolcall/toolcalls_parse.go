package toolcall

import (
	"strings"
)

type ParsedToolCall struct {
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

type ToolCallParseResult struct {
	Calls             []ParsedToolCall
	SawToolCallSyntax bool
	RejectedByPolicy  bool
	RejectedToolNames []string
}

func ParseToolCalls(text string, availableToolNames []string) []ParsedToolCall {
	return ParseToolCallsDetailed(text, availableToolNames).Calls
}

func ParseToolCallsDetailed(text string, availableToolNames []string) ToolCallParseResult {
	return parseToolCallsDetailedXMLOnly(text)
}

func ParseStandaloneToolCalls(text string, availableToolNames []string) []ParsedToolCall {
	return ParseStandaloneToolCallsDetailed(text, availableToolNames).Calls
}

func ParseStandaloneToolCallsDetailed(text string, availableToolNames []string) ToolCallParseResult {
	return parseToolCallsDetailedXMLOnly(text)
}

func ParseAssistantToolCallsDetailed(text, thinking string, availableToolNames []string) ToolCallParseResult {
	textParsed := ParseStandaloneToolCallsDetailed(text, availableToolNames)
	if len(textParsed.Calls) > 0 {
		return textParsed
	}
	if strings.TrimSpace(text) != "" {
		return textParsed
	}
	thinkingParsed := ParseStandaloneToolCallsDetailed(thinking, availableToolNames)
	if len(thinkingParsed.Calls) > 0 {
		return thinkingParsed
	}
	return textParsed
}

func parseToolCallsDetailedXMLOnly(text string) ToolCallParseResult {
	result := ToolCallParseResult{}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return result
	}
	trimmed = stripFencedCodeBlocks(trimmed)
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return result
	}

	normalized, ok := normalizeEPSEToolCallMarkup(trimmed)
	if !ok {
		return result
	}
	result.SawToolCallSyntax = looksLikeToolCallSyntax(normalized) || hasRepairableXMLToolCallsWrapper(normalized)
	parsed := parseXMLToolCalls(normalized)
	if len(parsed) == 0 && indexToolCDATAOpen(normalized, 0) >= 0 {
		recovered := SanitizeLooseCDATA(normalized)
		if recovered != normalized {
			parsed = parseXMLToolCalls(recovered)
		}
	}
	if len(parsed) == 0 {
		return result
	}

	result.SawToolCallSyntax = true
	calls, rejectedNames := filterToolCallsDetailed(parsed)
	result.Calls = calls
	result.RejectedToolNames = rejectedNames
	result.RejectedByPolicy = len(rejectedNames) > 0 && len(calls) == 0
	return result
}

func filterToolCallsDetailed(parsed []ParsedToolCall) ([]ParsedToolCall, []string) {
	out := make([]ParsedToolCall, 0, len(parsed))
	for _, tc := range parsed {
		if tc.Name == "" {
			continue
		}
		if tc.Input == nil {
			tc.Input = map[string]any{}
		}
		out = append(out, tc)
	}
	return out, nil
}

func looksLikeToolCallSyntax(text string) bool {
	hasEPSE, hasCanonical := ContainsToolCallWrapperSyntaxOutsideIgnored(text)
	return hasEPSE || hasCanonical
}

func stripFencedCodeBlocks(text string) string {
	if text == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(text))

	lines := strings.SplitAfter(text, "\n")
	inFence := false
	fenceMarker := ""
	inCDATA := false
	cdataFenceMarker := ""
	// Track builder length when a fence opens so we can preserve content
	// collected before the unclosed fence.
	beforeFenceLen := 0
	for _, line := range lines {
		if inCDATA || cdataStartsBeforeFence(line) {
			b.WriteString(line)
			inCDATA, cdataFenceMarker = updateCDATAStateForStrip(inCDATA, cdataFenceMarker, line)
			continue
		}
		trimmed := strings.TrimLeft(line, " \t")
		if !inFence {
			if marker, ok := parseFenceOpen(trimmed); ok {
				inFence = true
				fenceMarker = marker
				beforeFenceLen = b.Len()
				continue
			}
			b.WriteString(line)
			continue
		}

		if isFenceClose(trimmed, fenceMarker) {
			inFence = false
			fenceMarker = ""
		}
	}

	if inFence {
		// Unclosed fence: preserve content that was collected before the
		// fence started rather than dropping everything.
		result := b.String()
		if beforeFenceLen > 0 && beforeFenceLen <= len(result) {
			return result[:beforeFenceLen]
		}
		return ""
	}
	return b.String()
}

func markdownCodeSpanEnd(text string, start int) (int, bool) {
	if start < 0 || start >= len(text) || text[start] != '`' {
		return start, false
	}
	count := countLeadingFenceChars(text[start:], '`')
	if count == 0 {
		return start, false
	}
	search := start + count
	for search < len(text) {
		if text[search] != '`' {
			search++
			continue
		}
		run := countLeadingFenceChars(text[search:], '`')
		if run == count {
			return search + run, true
		}
		search += run
	}
	return start, false
}

func cdataStartsBeforeFence(line string) bool {
	cdataIdx := indexToolCDATAOpen(line, 0)
	if cdataIdx < 0 {
		return false
	}
	fenceIdx := firstFenceMarkerIndex(line)
	return fenceIdx < 0 || cdataIdx < fenceIdx
}

func firstFenceMarkerIndex(line string) int {
	idxBacktick := strings.Index(line, "```")
	idxTilde := strings.Index(line, "~~~")
	switch {
	case idxBacktick < 0:
		return idxTilde
	case idxTilde < 0:
		return idxBacktick
	case idxBacktick < idxTilde:
		return idxBacktick
	default:
		return idxTilde
	}
}

func updateCDATAStateForStrip(inCDATA bool, cdataFenceMarker, line string) (bool, string) {
	pos := 0
	state := inCDATA
	fenceMarker := cdataFenceMarker
	lineForFence := line
	if !state {
		start := indexToolCDATAOpen(line, pos)
		if start < 0 {
			return false, ""
		}
		pos = start + toolCDATAOpenLenAt(line, start)
		if pos > len(line) {
			pos = len(line)
		}
		state = true
		lineForFence = line[pos:]
	}
	if !state {
		return false, ""
	}

	trimmed := strings.TrimLeft(lineForFence, " \t")
	if fenceMarker == "" {
		if marker, ok := parseFenceOpen(trimmed); ok {
			fenceMarker = marker
		}
	} else if isFenceClose(trimmed, fenceMarker) {
		fenceMarker = ""
	}

	for pos < len(line) {
		endPos := -1
		closeLen := 0
		for search := pos; search < len(line); search++ {
			if foundLen := toolCDATACloseLenAt(line, search); foundLen > 0 {
				endPos = search
				closeLen = foundLen
				break
			}
		}
		if endPos < 0 {
			return true, fenceMarker
		}
		pos = endPos + closeLen
		if pos > len(line) {
			pos = len(line)
		}
		if fenceMarker != "" {
			continue
		}
		if cdataEndLooksStructural(line, pos) || strings.TrimSpace(line[pos:]) == "" {
			state = false
			for pos < len(line) {
				start := indexToolCDATAOpen(line, pos)
				if start < 0 {
					return false, ""
				}
				pos = start + toolCDATAOpenLenAt(line, start)
				if pos > len(line) {
					pos = len(line)
				}
				state = true
				trimmedTail := strings.TrimLeft(line[pos:], " \t")
				if marker, ok := parseFenceOpen(trimmedTail); ok {
					fenceMarker = marker
				} else {
					fenceMarker = ""
				}
				break
			}
			continue
		}
	}
	return state, fenceMarker
}

func parseFenceOpen(line string) (string, bool) {
	if len(line) < 3 {
		return "", false
	}
	ch := line[0]
	if ch != '`' && ch != '~' {
		return "", false
	}
	count := countLeadingFenceChars(line, ch)
	if count < 3 {
		return "", false
	}
	return strings.Repeat(string(ch), count), true
}

func isFenceClose(line, marker string) bool {
	if marker == "" {
		return false
	}
	ch := marker[0]
	if line == "" || line[0] != ch {
		return false
	}
	count := countLeadingFenceChars(line, ch)
	if count < len(marker) {
		return false
	}
	rest := strings.TrimSpace(line[count:])
	return rest == ""
}

func countLeadingFenceChars(line string, ch byte) int {
	count := 0
	for count < len(line) && line[count] == ch {
		count++
	}
	return count
}
