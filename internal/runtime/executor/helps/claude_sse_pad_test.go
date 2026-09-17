package helps

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestNeedsEmptyTextPad(t *testing.T) {
	thinkingState := ClaudeSSEPadState{SawThinking: true}
	tests := []struct {
		name       string
		thinking   string
		state      ClaudeSSEPadState
		stopReason string
		want       bool
	}{
		{name: "thinking-only end_turn", thinking: "reason", state: thinkingState, stopReason: "end_turn", want: true},
		{name: "thinking-only STOP", thinking: "reason", state: thinkingState, stopReason: "STOP", want: true},
		{name: "thinking-only empty stop", thinking: "reason", state: thinkingState, stopReason: "", want: true},
		{name: "saw thinking flag without text", state: thinkingState, stopReason: "end_turn", want: true},
		{name: "max_tokens", thinking: "reason", state: thinkingState, stopReason: "max_tokens", want: false},
		{name: "MAX_TOKENS", thinking: "reason", state: thinkingState, stopReason: "MAX_TOKENS", want: false},
		{name: "saw text", thinking: "reason", state: ClaudeSSEPadState{SawThinking: true, SawText: true}, stopReason: "end_turn", want: false},
		{name: "saw tool_use", thinking: "reason", state: ClaudeSSEPadState{SawThinking: true, SawToolUse: true}, stopReason: "end_turn", want: false},
		{name: "empty stream", stopReason: "end_turn", want: false},
		{name: "already padded", thinking: "reason", state: ClaudeSSEPadState{SawThinking: true, Padded: true}, stopReason: "end_turn", want: false},
		{name: "tool_use stop reason", thinking: "reason", state: thinkingState, stopReason: "tool_use", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := NeedsEmptyTextPad(test.thinking, test.state, test.stopReason); got != test.want {
				t.Fatalf("NeedsEmptyTextPad() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestNormalSilentStop(t *testing.T) {
	tests := []struct {
		stopReason string
		want       bool
	}{
		{stopReason: "", want: true},
		{stopReason: "end_turn", want: true},
		{stopReason: "STOP", want: true},
		{stopReason: "max_tokens", want: false},
		{stopReason: "MAX_TOKENS", want: false},
		{stopReason: "tool_use", want: false},
	}
	for _, test := range tests {
		name := test.stopReason
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			if got := NormalSilentStop(test.stopReason); got != test.want {
				t.Fatalf("NormalSilentStop(%q) = %v, want %v", test.stopReason, got, test.want)
			}
		})
	}
}

func TestNextPadIndex(t *testing.T) {
	if got := (ClaudeSSEPadState{}).NextPadIndex(); got != 0 {
		t.Fatalf("NextPadIndex() without index = %d, want 0", got)
	}
	if got := (ClaudeSSEPadState{HasIndex: true, MaxIndex: 0}).NextPadIndex(); got != 1 {
		t.Fatalf("NextPadIndex() after index 0 = %d, want 1", got)
	}
	if got := (ClaudeSSEPadState{HasIndex: true, MaxIndex: 2}).NextPadIndex(); got != 3 {
		t.Fatalf("NextPadIndex() after index 2 = %d, want 3", got)
	}
}

func TestObserveTracksContentAndIndexes(t *testing.T) {
	var state ClaudeSSEPadState
	state.Observe("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`)
	state.Observe("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"reason"}}`)
	state.Observe("content_block_stop", `{"type":"content_block_stop","index":0}`)
	if !state.SawThinking || state.SawText || state.SawToolUse {
		t.Fatalf("thinking observation = %+v", state)
	}
	if !state.HasIndex || state.MaxIndex != 0 || state.NextPadIndex() != 1 {
		t.Fatalf("index tracking = %+v", state)
	}

	state.Observe("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`)
	if !state.SawText || state.MaxIndex != 1 {
		t.Fatalf("text observation = %+v", state)
	}

	var tools ClaudeSSEPadState
	tools.Observe("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"run","input":{}}}`)
	tools.Observe("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`)
	if !tools.SawToolUse {
		t.Fatalf("tool observation = %+v", tools)
	}
}

func TestFormatEmptyTextBlock(t *testing.T) {
	got := string(FormatEmptyTextBlock(1, 3))
	want := "event: content_block_start\n" +
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}` +
		"\n\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":1}` +
		"\n\n\n"
	if got != want {
		t.Fatalf("FormatEmptyTextBlock() = %q, want %q", got, want)
	}
	appended := string(AppendEmptyTextBlock([]byte("prefix"), 0, 3))
	if !strings.HasPrefix(appended, "prefix") || !strings.Contains(appended, `"index":0`) {
		t.Fatalf("AppendEmptyTextBlock() = %q", appended)
	}
}

func TestAppendEmptyTextContentIfNeeded(t *testing.T) {
	thinking := []byte(`{"type":"thinking","thinking":"reason"}`)
	text := []byte(`{"type":"text","text":"hello"}`)
	tool := []byte(`{"type":"tool_use","id":"toolu_1","name":"run","input":{}}`)

	padded := AppendEmptyTextContentIfNeeded([][]byte{thinking}, "end_turn")
	if len(padded) != 2 {
		t.Fatalf("thinking-only end_turn len = %d, want 2", len(padded))
	}
	if got := gjson.GetBytes(padded[1], "type").String(); got != "text" {
		t.Fatalf("pad type = %q, want text", got)
	}
	if got := gjson.GetBytes(padded[1], "text").String(); got != "" {
		t.Fatalf("pad text = %q, want empty", got)
	}

	paddedStop := AppendEmptyTextContentIfNeeded([][]byte{thinking}, "STOP")
	if len(paddedStop) != 2 {
		t.Fatalf("thinking-only STOP len = %d, want 2", len(paddedStop))
	}
	paddedEmpty := AppendEmptyTextContentIfNeeded([][]byte{thinking}, "")
	if len(paddedEmpty) != 2 {
		t.Fatalf("thinking-only empty stop len = %d, want 2", len(paddedEmpty))
	}

	if got := AppendEmptyTextContentIfNeeded([][]byte{thinking}, "max_tokens"); len(got) != 1 {
		t.Fatalf("max_tokens len = %d, want 1", len(got))
	}
	if got := AppendEmptyTextContentIfNeeded([][]byte{thinking, text}, "end_turn"); len(got) != 2 {
		t.Fatalf("existing text len = %d, want 2", len(got))
	}
	if got := AppendEmptyTextContentIfNeeded([][]byte{thinking, tool}, "end_turn"); len(got) != 2 {
		t.Fatalf("tool_use len = %d, want 2", len(got))
	}
	if got := AppendEmptyTextContentIfNeeded(nil, "end_turn"); len(got) != 0 {
		t.Fatalf("empty stream len = %d, want 0", len(got))
	}
}

func TestInjectReasoningOnlyEmptyText(t *testing.T) {
	sse := appendClaudeSSEEventString(nil, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`, 3)
	sse = appendClaudeSSEEventString(sse, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"reason"}}`, 3)
	sse = appendClaudeSSEEventString(sse, "content_block_stop", `{"type":"content_block_stop","index":0}`, 3)
	sse = appendClaudeSSEEventString(sse, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null}}`, 3)
	sse = appendClaudeSSEEventString(sse, "message_stop", `{"type":"message_stop"}`, 3)

	got := string(InjectReasoningOnlyEmptyText(sse, "reason", 3))
	emptyTextAt := strings.Index(got, `"content_block":{"type":"text","text":""}`)
	deltaAt := strings.Index(got, `"type":"message_delta"`)
	if emptyTextAt < 0 {
		t.Fatalf("expected empty text pad, got:\n%s", got)
	}
	if emptyTextAt > deltaAt {
		t.Fatalf("empty text pad must appear before message_delta:\n%s", got)
	}
	if strings.Contains(got, `"stop_reason":"max_tokens"`) {
		t.Fatalf("must not forge max_tokens:\n%s", got)
	}

	truncated := appendClaudeSSEEventString(nil, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`, 3)
	truncated = appendClaudeSSEEventString(truncated, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null}}`, 3)
	if strings.Contains(string(InjectReasoningOnlyEmptyText(truncated, "reason", 3)), `"content_block":{"type":"text"`) {
		t.Fatalf("max_tokens must not pad")
	}
}

func TestClaudeSSEStreamPadAcrossChunks(t *testing.T) {
	pad := NewClaudeSSEStreamPad(sdktranslator.FormatClaude)
	if pad == nil {
		t.Fatal("expected Claude stream pad")
	}
	if NewClaudeSSEStreamPad(sdktranslator.FormatOpenAI) != nil {
		t.Fatal("non-Claude format must not create pad")
	}

	thinking := appendClaudeSSEEventString(nil, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`, 3)
	thinking = appendClaudeSSEEventString(thinking, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"reason"}}`, 3)
	thinking = appendClaudeSSEEventString(thinking, "content_block_stop", `{"type":"content_block_stop","index":0}`, 3)
	delta := appendClaudeSSEEventString(nil, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null}}`, 3)
	stop := appendClaudeSSEEventString(nil, "message_stop", `{"type":"message_stop"}`, 3)

	chunks := ProcessClaudeSSEPadChunks(pad, [][]byte{thinking, delta, stop})
	joined := string(bytesJoin(chunks))
	emptyTextAt := strings.Index(joined, `"content_block":{"type":"text","text":""}`)
	deltaAt := strings.Index(joined, `"type":"message_delta"`)
	if emptyTextAt < 0 || emptyTextAt > deltaAt {
		t.Fatalf("expected empty text pad before message_delta:\n%s", joined)
	}
}

func TestClaudeSSEStreamPadMaxTokensAcrossChunksDoesNotPad(t *testing.T) {
	pad := NewClaudeSSEStreamPad(sdktranslator.FormatClaude)
	thinking := appendClaudeSSEEventString(nil, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`, 3)
	thinking = appendClaudeSSEEventString(thinking, "content_block_stop", `{"type":"content_block_stop","index":0}`, 3)
	delta := appendClaudeSSEEventString(nil, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null}}`, 3)
	stop := appendClaudeSSEEventString(nil, "message_stop", `{"type":"message_stop"}`, 3)

	joined := string(bytesJoin(ProcessClaudeSSEPadChunks(pad, [][]byte{thinking, delta, stop})))
	if strings.Contains(joined, `"content_block":{"type":"text"`) {
		t.Fatalf("max_tokens must not pad across chunks:\n%s", joined)
	}
}

func TestPadClaudeMessagePayload(t *testing.T) {
	payload := []byte(`{"type":"message","role":"assistant","content":[{"type":"thinking","thinking":"reason"}],"stop_reason":"end_turn"}`)
	got := PadClaudeMessagePayload(sdktranslator.FormatClaude, payload)
	blocks := gjson.GetBytes(got, "content").Array()
	if len(blocks) != 2 || blocks[1].Get("type").String() != "text" || blocks[1].Get("text").String() != "" {
		t.Fatalf("expected thinking + empty text, got: %s", got)
	}

	maxTokens := []byte(`{"type":"message","role":"assistant","content":[{"type":"thinking","thinking":"reason"}],"stop_reason":"max_tokens"}`)
	if padded := PadClaudeMessagePayload(sdktranslator.FormatClaude, maxTokens); string(padded) != string(maxTokens) {
		t.Fatalf("max_tokens must not pad: %s", padded)
	}
	if padded := PadClaudeMessagePayload(sdktranslator.FormatOpenAI, payload); string(padded) != string(payload) {
		t.Fatalf("non-Claude format must not pad")
	}
}

func bytesJoin(chunks [][]byte) []byte {
	var out []byte
	for _, chunk := range chunks {
		out = append(out, chunk...)
	}
	return out
}
