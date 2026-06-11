package helps

import (
	"bytes"
	"encoding/json"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// SSENormalizer buffers and reorders Claude SSE events to ensure
// message_stop is always the last event in the stream.
//
// Some upstream models (e.g. Baidu-glm-5.1, Tencent-glm-5.1) emit
// content blocks after message_stop, violating the Anthropic SSE protocol:
//
//	...content_block_* → message_delta → message_stop → content_block_* → message_delta
//
// Strategy: once we see the first message_delta (which signals the start of
// the stream epilog), we buffer everything. On Flush, we reorder the buffer
// so all content_block events come first, then message_delta, then message_stop.
// For correctly-ordered streams, this is a no-op (buffer is already in order).
type SSENormalizer struct {
	buffering bool // true after first message_delta is seen
	buffer    [][]byte
}

// sseEventType extracts the event type from an SSE "event:" line.
// Handles both "event: type" and "event:type" with optional extra whitespace.
func sseEventType(line []byte) string {
	trimmed := bytes.TrimSpace(line)
	if bytes.HasPrefix(trimmed, []byte("event:")) {
		return string(bytes.TrimSpace(bytes.TrimPrefix(trimmed, []byte("event:"))))
	}
	return ""
}

// ProcessLine processes a single SSE line and returns lines to emit.
// After the first message_delta, all subsequent lines are buffered
// until Flush is called.
func (n *SSENormalizer) ProcessLine(line []byte) [][]byte {
	eventType := sseEventType(line)

	// Start buffering when we see the first message_delta.
	// message_delta is only emitted in the epilog of a Claude stream,
	// so all subsequent events (including message_stop and any
	// misplaced content blocks) should be buffered for reordering.
	if eventType == "message_delta" && !n.buffering {
		n.buffering = true
	}

	if n.buffering {
		// A new message_start means a brand new message; flush current buffer first.
		if eventType == "message_start" {
			result := n.Flush()
			return append(result, line)
		}
		n.buffer = append(n.buffer, append([]byte(nil), line...))
		return nil
	}

	return [][]byte{line}
}

// Flush emits all buffered events in the correct order.
// Content block events come first, then message_delta, then message_stop.
// Must be called after the last ProcessLine.
func (n *SSENormalizer) Flush() [][]byte {
	if !n.buffering || len(n.buffer) == 0 {
		n.buffering = false
		n.buffer = nil
		return nil
	}

	// Classify buffered lines by tracking the current event: type.
	currentType := ""
	var contentLines [][]byte
	var deltaLines [][]byte
	var stopLines [][]byte
	var otherLines [][]byte

	for _, line := range n.buffer {
		et := sseEventType(line)
		if et != "" {
			currentType = et
		}

		switch currentType {
		case "content_block_start", "content_block_delta", "content_block_stop":
			contentLines = append(contentLines, line)
		case "message_delta":
			deltaLines = append(deltaLines, line)
		case "message_stop":
			stopLines = append(stopLines, line)
		default:
			otherLines = append(otherLines, line)
		}
	}

	var result [][]byte
	result = append(result, contentLines...)
	result = append(result, deltaLines...)
	result = append(result, stopLines...)
	result = append(result, otherLines...)

	n.buffering = false
	n.buffer = nil

	return result
}

// NormalizeNonStreamContentOrder reorders content blocks in a non-stream
// Claude API response so that thinking blocks come before text blocks,
// matching the Anthropic protocol convention.
//
// Some models return content in [text, thinking] order instead of
// [thinking, text]. This function normalizes to [thinking, text, tool_use].
func NormalizeNonStreamContentOrder(data []byte) []byte {
	content := gjson.GetBytes(data, "content")
	if !content.Exists() || !content.IsArray() {
		return data
	}

	arr := content.Array()
	if len(arr) <= 1 {
		return data
	}

	// Check if any thinking block appears after a text block.
	needReorder := false
	seenText := false
	for _, block := range arr {
		t := block.Get("type").String()
		if t == "text" {
			seenText = true
		}
		if t == "thinking" && seenText {
			needReorder = true
			break
		}
	}
	if !needReorder {
		return data
	}

	// Reorder: thinking blocks first, then text, then tool_use, then others.
	var thinkingBlocks []interface{}
	var textBlocks []interface{}
	var toolUseBlocks []interface{}
	var otherBlocks []interface{}

	for _, block := range arr {
		t := block.Get("type").String()
		raw := json.RawMessage(block.Raw)
		switch t {
		case "thinking":
			thinkingBlocks = append(thinkingBlocks, raw)
		case "text":
			textBlocks = append(textBlocks, raw)
		case "tool_use":
			toolUseBlocks = append(toolUseBlocks, raw)
		default:
			otherBlocks = append(otherBlocks, raw)
		}
	}

	var reordered []interface{}
	reordered = append(reordered, thinkingBlocks...)
	reordered = append(reordered, textBlocks...)
	reordered = append(reordered, toolUseBlocks...)
	reordered = append(reordered, otherBlocks...)

	result, err := sjson.SetBytes(data, "content", reordered)
	if err != nil {
		return data
	}
	return result
}
