package common

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// ClaudeSSEPadState tracks Claude SSE content seen so far so a thinking-only
// normal stop can receive a legal empty text block without forging truncation.
type ClaudeSSEPadState struct {
	SawText     bool
	SawToolUse  bool
	SawThinking bool
	HasIndex    bool
	MaxIndex    int
	Padded      bool
}

// Observe records text, thinking, tool_use, and content-block indexes from one SSE event.
func (s *ClaudeSSEPadState) Observe(event, data string) {
	if s == nil {
		return
	}
	typ := event
	if typ == "" {
		typ = gjson.Get(data, "type").String()
	}
	if idx := gjson.Get(data, "index"); idx.Exists() {
		s.HasIndex = true
		n := int(idx.Int())
		if n > s.MaxIndex {
			s.MaxIndex = n
		}
	}
	switch typ {
	case "content_block_start":
		switch gjson.Get(data, "content_block.type").String() {
		case "text":
			s.SawText = true
		case "tool_use":
			s.SawToolUse = true
		case "thinking":
			s.SawThinking = true
		}
	case "content_block_delta":
		switch gjson.Get(data, "delta.type").String() {
		case "text_delta":
			s.SawText = true
		case "thinking_delta":
			s.SawThinking = true
		case "input_json_delta":
			s.SawToolUse = true
		}
	}
}

// NextPadIndex returns the content-block index for a newly injected empty text block.
func (s ClaudeSSEPadState) NextPadIndex() int {
	if !s.HasIndex {
		return 0
	}
	return s.MaxIndex + 1
}

// NormalSilentStop reports whether stopReason is a normal completed turn
// (end_turn, STOP, or empty) rather than truncation or a tool turn.
func NormalSilentStop(stopReason string) bool {
	switch stopReason {
	case "", "end_turn", "STOP":
		return true
	default:
		return false
	}
}

// NeedsEmptyTextPad reports whether a legal empty Claude text block should be
// injected so clients can complete a thinking-only normal stop.
func NeedsEmptyTextPad(thinking string, state ClaudeSSEPadState, stopReason string) bool {
	if state.Padded || state.SawText || state.SawToolUse {
		return false
	}
	if !state.SawThinking && strings.TrimSpace(thinking) == "" {
		return false
	}
	return NormalSilentStop(stopReason)
}

// EmptyTextBlockIndexAfterClose returns the index for an empty text pad after
// optionally closing the current content block. Translators that do not
// increment ResponseIndex on the terminal close should pass blockOpen=true.
func EmptyTextBlockIndexAfterClose(currentIndex int, blockOpen bool) int {
	if blockOpen {
		return currentIndex + 1
	}
	return currentIndex
}

// FormatEmptyTextBlock returns the Claude SSE empty text start+stop events.
func FormatEmptyTextBlock(index, trailingNewlines int) []byte {
	return AppendEmptyTextBlock(nil, index, trailingNewlines)
}

// AppendEmptyTextBlock appends a legal empty Claude text content block.
func AppendEmptyTextBlock(out []byte, index, trailingNewlines int) []byte {
	start := make([]byte, 0, 88)
	start = append(start, `{"type":"content_block_start","index":`...)
	start = strconv.AppendInt(start, int64(index), 10)
	start = append(start, `,"content_block":{"type":"text","text":""}}`...)
	out = AppendSSEEventBytes(out, "content_block_start", start, trailingNewlines)

	stop := make([]byte, 0, 48)
	stop = append(stop, `{"type":"content_block_stop","index":`...)
	stop = strconv.AppendInt(stop, int64(index), 10)
	stop = append(stop, '}')
	return AppendSSEEventBytes(out, "content_block_stop", stop, trailingNewlines)
}

// AppendEmptyTextContentIfNeeded appends a legal empty text content block to a
// non-stream Claude content array when the turn is thinking-only and normal.
func AppendEmptyTextContentIfNeeded(blocks [][]byte, stopReason string) [][]byte {
	state := ClaudeSSEPadState{}
	thinking := ""
	for _, block := range blocks {
		switch gjson.GetBytes(block, "type").String() {
		case "text":
			state.SawText = true
		case "tool_use":
			state.SawToolUse = true
		case "thinking":
			state.SawThinking = true
			if thinking == "" {
				thinking = gjson.GetBytes(block, "thinking").String()
			}
		}
	}
	if !NeedsEmptyTextPad(thinking, state, stopReason) {
		return blocks
	}
	return append(blocks, []byte(`{"type":"text","text":""}`))
}

// InjectReasoningOnlyEmptyText rewrites Claude SSE bytes so a thinking-only
// normal stop includes an empty text content block before message_delta or
// message_stop. Callers that already finalize in-place can use
// NeedsEmptyTextPad + AppendEmptyTextBlock instead.
func InjectReasoningOnlyEmptyText(sse []byte, thinking string, trailingNewlines int) []byte {
	if len(sse) == 0 {
		return sse
	}
	state := ClaudeSSEPadState{}
	stopReason := ""
	insertAt := -1
	pos := 0
	for pos < len(sse) {
		event, data, next := readClaudeSSEEvent(sse, pos)
		if next == pos {
			break
		}
		if insertAt < 0 && (event == "message_delta" || event == "message_stop") {
			insertAt = pos
			if event == "message_delta" {
				stopReason = gjson.Get(data, "delta.stop_reason").String()
			}
		}
		if insertAt < 0 {
			state.Observe(event, data)
		}
		pos = next
	}
	if insertAt < 0 || !NeedsEmptyTextPad(thinking, state, stopReason) {
		return sse
	}
	pad := AppendEmptyTextBlock(nil, state.NextPadIndex(), trailingNewlines)
	out := make([]byte, 0, len(sse)+len(pad))
	out = append(out, sse[:insertAt]...)
	out = append(out, pad...)
	out = append(out, sse[insertAt:]...)
	return out
}

func readClaudeSSEEvent(sse []byte, pos int) (event, data string, next int) {
	for pos < len(sse) && sse[pos] == '\n' {
		pos++
	}
	if pos >= len(sse) {
		return "", "", pos
	}
	lineEnd := bytes.IndexByte(sse[pos:], '\n')
	if lineEnd < 0 {
		line := sse[pos:]
		if bytes.HasPrefix(line, []byte("event: ")) {
			return string(line[len("event: "):]), "", len(sse)
		}
		if bytes.HasPrefix(line, []byte("data: ")) {
			return "", string(line[len("data: "):]), len(sse)
		}
		return "", "", len(sse)
	}
	line := sse[pos : pos+lineEnd]
	next = pos + lineEnd + 1
	if bytes.HasPrefix(line, []byte("event: ")) {
		event = string(line[len("event: "):])
		if next < len(sse) && bytes.HasPrefix(sse[next:], []byte("data: ")) {
			dataLineEnd := bytes.IndexByte(sse[next:], '\n')
			if dataLineEnd < 0 {
				data = string(sse[next+len("data: "):])
				return event, data, len(sse)
			}
			data = string(sse[next+len("data: ") : next+dataLineEnd])
			next = next + dataLineEnd + 1
		}
		return event, data, next
	}
	if bytes.HasPrefix(line, []byte("data: ")) {
		return "", string(line[len("data: "):]), next
	}
	return "", "", next
}
