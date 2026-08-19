package sse

import (
	"fmt"
	"strings"
)

// LineResult is the normalized parse result for one DeepSeek SSE line.
type LineResult struct {
	Parsed                     bool
	Stop                       bool
	ContentFilter              bool
	ErrorMessage               string
	Parts                      []ContentPart
	ToolDetectionThinkingParts []ContentPart
	NextType                   string
	ResponseMessageID          int
}

// ParseDeepSeekContentLine centralizes one-line DeepSeek SSE parsing for both
// streaming and non-streaming handlers.
func ParseDeepSeekContentLine(raw []byte, thinkingEnabled bool, currentType string) LineResult {
	chunk, done, parsed := ParseDeepSeekSSELine(raw)
	if !parsed {
		return LineResult{NextType: currentType}
	}
	if done {
		return LineResult{Parsed: true, Stop: true, NextType: currentType}
	}
	if errObj, hasErr := chunk["error"]; hasErr {
		return LineResult{
			Parsed:       true,
			Stop:         true,
			ErrorMessage: fmt.Sprintf("%v", errObj),
			NextType:     currentType,
		}
	}
	if code, _ := chunk["code"].(string); code == "content_filter" {
		return LineResult{
			Parsed:        true,
			Stop:          true,
			ContentFilter: true,
			NextType:      currentType,
		}
	}
	if hasContentFilterStatus(chunk) {
		return LineResult{
			Parsed:        true,
			Stop:          true,
			ContentFilter: true,
			NextType:      currentType,
		}
	}
	if msg, ok := extractHintError(chunk); ok {
		return LineResult{
			Parsed:       true,
			Stop:         true,
			ErrorMessage: msg,
			NextType:     currentType,
		}
	}
	parts, detectionThinkingParts, finished, nextType := ParseSSEChunkForContentDetailed(chunk, thinkingEnabled, currentType)
	parts = filterLeakedContentFilterParts(parts)
	detectionThinkingParts = filterLeakedContentFilterParts(detectionThinkingParts)
	var respMsgID int
	observeResponseMessageID(chunk, &respMsgID)
	return LineResult{
		Parsed:                     true,
		Stop:                       finished,
		Parts:                      parts,
		ToolDetectionThinkingParts: detectionThinkingParts,
		NextType:                   nextType,
		ResponseMessageID:          respMsgID,
	}
}

// extractHintError detects DeepSeek "event: hint" error payloads such as
// {"type":"error","content":"内容超长，请删减后再试","finish_reason":"input_exceeds_limit"}.
// These arrive as ordinary data: lines but carry a top-level type=error with a
// human-readable content field. Normal content chunks never have a top-level
// type field (fragment types live inside nested v arrays), so this is safe.
func extractHintError(chunk map[string]any) (string, bool) {
	typeVal, _ := chunk["type"].(string)
	if !strings.EqualFold(strings.TrimSpace(typeVal), "error") {
		return "", false
	}
	content, _ := chunk["content"].(string)
	content = strings.TrimSpace(content)
	if content == "" {
		return "", false
	}
	return content, true
}
