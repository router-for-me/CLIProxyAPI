package helps

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
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

// ClaudeSSEStreamPad post-processes translated Claude SSE chunks after leaving
// the translator, injecting an empty text block before message_delta
// when the turn is thinking-only and normally stopped.
type ClaudeSSEStreamPad struct {
	state            ClaudeSSEPadState
	sawMessageDelta  bool
	TrailingNewlines int
}

// NewClaudeSSEStreamPad returns a stream padder when responseFormat is Claude.
func NewClaudeSSEStreamPad(responseFormat sdktranslator.Format) *ClaudeSSEStreamPad {
	if responseFormat != sdktranslator.FormatClaude {
		return nil
	}
	return &ClaudeSSEStreamPad{TrailingNewlines: 3}
}

// ProcessChunks applies thinking-only empty-text padding to translated Claude SSE chunks.
func ProcessClaudeSSEPadChunks(pad *ClaudeSSEStreamPad, chunks [][]byte) [][]byte {
	if pad == nil || len(chunks) == 0 {
		return chunks
	}
	out := make([][]byte, 0, len(chunks))
	for _, chunk := range chunks {
		out = append(out, pad.Process(chunk))
	}
	return out
}

// Process rewrites one Claude SSE chunk, injecting an empty text pad when needed.
func (p *ClaudeSSEStreamPad) Process(chunk []byte) []byte {
	if p == nil || len(chunk) == 0 || p.state.Padded {
		return chunk
	}
	trailing := p.TrailingNewlines
	if trailing <= 0 {
		trailing = 3
	}
	return p.inject(chunk, trailing)
}

func (p *ClaudeSSEStreamPad) inject(sse []byte, trailingNewlines int) []byte {
	// Only pad immediately before message_delta, which carries stop_reason.
	// A later message_stop chunk must not re-evaluate with an empty stop reason
	// after a max_tokens/tool_use message_delta already passed without padding.
	if p.sawMessageDelta || p.state.Padded {
		return sse
	}
	stopReason := ""
	insertAt := -1
	pos := 0
	for pos < len(sse) {
		event, data, next := readClaudeSSEEvent(sse, pos)
		if next == pos {
			break
		}
		if insertAt < 0 && event == "message_delta" {
			insertAt = pos
			stopReason = gjson.Get(data, "delta.stop_reason").String()
			p.sawMessageDelta = true
		}
		if insertAt < 0 {
			p.state.Observe(event, data)
		}
		pos = next
	}
	if insertAt < 0 || !NeedsEmptyTextPad("", p.state, stopReason) {
		return sse
	}
	pad := AppendEmptyTextBlock(nil, p.state.NextPadIndex(), trailingNewlines)
	p.state.Padded = true
	p.state.SawText = true
	out := make([]byte, 0, len(sse)+len(pad))
	out = append(out, sse[:insertAt]...)
	out = append(out, pad...)
	out = append(out, sse[insertAt:]...)
	return out
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
	out = appendClaudeSSEEventBytes(out, "content_block_start", start, trailingNewlines)

	stop := make([]byte, 0, 48)
	stop = append(stop, `{"type":"content_block_stop","index":`...)
	stop = strconv.AppendInt(stop, int64(index), 10)
	stop = append(stop, '}')
	return appendClaudeSSEEventBytes(out, "content_block_stop", stop, trailingNewlines)
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

// PadClaudeMessagePayload appends an empty text content block to a Claude
// Messages JSON body when responseFormat is Claude and the turn is thinking-only.
func PadClaudeMessagePayload(responseFormat sdktranslator.Format, payload []byte) []byte {
	if responseFormat != sdktranslator.FormatClaude || len(payload) == 0 {
		return payload
	}
	stopReason := gjson.GetBytes(payload, "stop_reason").String()
	content := gjson.GetBytes(payload, "content")
	if !content.IsArray() {
		return payload
	}
	state := ClaudeSSEPadState{}
	thinking := ""
	content.ForEach(func(_, value gjson.Result) bool {
		switch value.Get("type").String() {
		case "text":
			state.SawText = true
		case "tool_use":
			state.SawToolUse = true
		case "thinking":
			state.SawThinking = true
			if thinking == "" {
				thinking = value.Get("thinking").String()
			}
		}
		return true
	})
	if !NeedsEmptyTextPad(thinking, state, stopReason) {
		return payload
	}
	padded, errSet := sjson.SetRawBytes(payload, "content.-1", []byte(`{"type":"text","text":""}`))
	if errSet != nil {
		return payload
	}
	return padded
}

// InjectReasoningOnlyEmptyText rewrites Claude SSE bytes so a thinking-only
// normal stop includes an empty text content block before message_delta or
// message_stop.
func InjectReasoningOnlyEmptyText(sse []byte, thinking string, trailingNewlines int) []byte {
	if len(sse) == 0 {
		return sse
	}
	pad := &ClaudeSSEStreamPad{TrailingNewlines: trailingNewlines}
	// Seed thinking visibility from caller when SSE observation alone is insufficient.
	if strings.TrimSpace(thinking) != "" {
		pad.state.SawThinking = true
	}
	return pad.Process(sse)
}

func appendClaudeSSEEventBytes(out []byte, event string, payload []byte, trailingNewlines int) []byte {
	out = append(out, "event: "...)
	out = append(out, event...)
	out = append(out, '\n')
	out = append(out, "data: "...)
	out = append(out, payload...)
	for i := 0; i < trailingNewlines; i++ {
		out = append(out, '\n')
	}
	return out
}

func appendClaudeSSEEventString(out []byte, event, payload string, trailingNewlines int) []byte {
	return appendClaudeSSEEventBytes(out, event, []byte(payload), trailingNewlines)
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
