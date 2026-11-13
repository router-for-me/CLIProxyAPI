package chat_completions

import (
	"bytes"
	"context"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func assertCachedCreationTokens(t *testing.T, payload []byte, want int64) {
	t.Helper()

	got := ojson.GetBytesNoCopy(payload, "usage.prompt_tokens_details.cached_creation_tokens")
	if !got.Exists() {
		t.Fatalf("expected cached_creation_tokens to exist, payload=%s", string(payload))
	}
	if got.Int() != want {
		t.Fatalf("expected cached_creation_tokens %d, got %d", want, got.Int())
	}
}

func TestConvertClaudeResponseToOpenAI_StreamUsageIncludesCachedTokens(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertClaudeResponseToOpenAI(
		ctx,
		"claude-opus-4-6",
		nil,
		nil,
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":13,"output_tokens":4,"cache_read_input_tokens":22000,"cache_creation_input_tokens":31}}`),
		&param,
	)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	if gotPromptTokens := ojson.GetBytesNoCopy(out[0], "usage.prompt_tokens").Int(); gotPromptTokens != 22044 {
		t.Fatalf("expected prompt_tokens %d, got %d", 22044, gotPromptTokens)
	}
	if gotCompletionTokens := ojson.GetBytesNoCopy(out[0], "usage.completion_tokens").Int(); gotCompletionTokens != 4 {
		t.Fatalf("expected completion_tokens %d, got %d", 4, gotCompletionTokens)
	}
	if gotTotalTokens := ojson.GetBytesNoCopy(out[0], "usage.total_tokens").Int(); gotTotalTokens != 22048 {
		t.Fatalf("expected total_tokens %d, got %d", 22048, gotTotalTokens)
	}
	if gotCachedTokens := ojson.GetBytesNoCopy(out[0], "usage.prompt_tokens_details.cached_tokens").Int(); gotCachedTokens != 22000 {
		t.Fatalf("expected cached_tokens %d, got %d", 22000, gotCachedTokens)
	}
	assertCachedCreationTokens(t, out[0], 31)
}

func TestConvertClaudeResponseToOpenAI_StreamUsageMergesMessageStartUsage(t *testing.T) {
	ctx := context.Background()
	var param any

	ConvertClaudeResponseToOpenAI(
		ctx,
		"claude-opus-4-6",
		nil,
		nil,
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","model":"claude-opus-4-6","usage":{"input_tokens":13,"output_tokens":1,"cache_read_input_tokens":22000,"cache_creation_input_tokens":31}}}`),
		&param,
	)
	out := ConvertClaudeResponseToOpenAI(
		ctx,
		"claude-opus-4-6",
		nil,
		nil,
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`),
		&param,
	)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	if gotPromptTokens := ojson.GetBytesNoCopy(out[0], "usage.prompt_tokens").Int(); gotPromptTokens != 22044 {
		t.Fatalf("expected prompt_tokens %d, got %d", 22044, gotPromptTokens)
	}
	if gotCompletionTokens := ojson.GetBytesNoCopy(out[0], "usage.completion_tokens").Int(); gotCompletionTokens != 4 {
		t.Fatalf("expected completion_tokens %d, got %d", 4, gotCompletionTokens)
	}
	if gotTotalTokens := ojson.GetBytesNoCopy(out[0], "usage.total_tokens").Int(); gotTotalTokens != 22048 {
		t.Fatalf("expected total_tokens %d, got %d", 22048, gotTotalTokens)
	}
	if gotCachedTokens := ojson.GetBytesNoCopy(out[0], "usage.prompt_tokens_details.cached_tokens").Int(); gotCachedTokens != 22000 {
		t.Fatalf("expected cached_tokens %d, got %d", 22000, gotCachedTokens)
	}
	assertCachedCreationTokens(t, out[0], 31)
}

func TestConvertClaudeResponseToOpenAINonStream_UsageIncludesCachedTokens(t *testing.T) {
	rawJSON := []byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"model\":\"claude-opus-4-6\"}}\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":13,\"output_tokens\":4,\"cache_read_input_tokens\":22000,\"cache_creation_input_tokens\":31}}\n")

	out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "", nil, nil, rawJSON, nil)

	if gotPromptTokens := ojson.GetBytesNoCopy(out, "usage.prompt_tokens").Int(); gotPromptTokens != 22044 {
		t.Fatalf("expected prompt_tokens %d, got %d", 22044, gotPromptTokens)
	}
	if gotCompletionTokens := ojson.GetBytesNoCopy(out, "usage.completion_tokens").Int(); gotCompletionTokens != 4 {
		t.Fatalf("expected completion_tokens %d, got %d", 4, gotCompletionTokens)
	}
	if gotTotalTokens := ojson.GetBytesNoCopy(out, "usage.total_tokens").Int(); gotTotalTokens != 22048 {
		t.Fatalf("expected total_tokens %d, got %d", 22048, gotTotalTokens)
	}
	if gotCachedTokens := ojson.GetBytesNoCopy(out, "usage.prompt_tokens_details.cached_tokens").Int(); gotCachedTokens != 22000 {
		t.Fatalf("expected cached_tokens %d, got %d", 22000, gotCachedTokens)
	}
	assertCachedCreationTokens(t, out, 31)
}

func TestConvertClaudeResponseToOpenAINonStream_UsageMergesMessageStartUsage(t *testing.T) {
	rawJSON := []byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"model\":\"claude-opus-4-6\",\"usage\":{\"input_tokens\":13,\"output_tokens\":1,\"cache_read_input_tokens\":22000,\"cache_creation_input_tokens\":31}}}\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":4}}\n")

	out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "", nil, nil, rawJSON, nil)

	if gotPromptTokens := ojson.GetBytesNoCopy(out, "usage.prompt_tokens").Int(); gotPromptTokens != 22044 {
		t.Fatalf("expected prompt_tokens %d, got %d", 22044, gotPromptTokens)
	}
	if gotCompletionTokens := ojson.GetBytesNoCopy(out, "usage.completion_tokens").Int(); gotCompletionTokens != 4 {
		t.Fatalf("expected completion_tokens %d, got %d", 4, gotCompletionTokens)
	}
	if gotTotalTokens := ojson.GetBytesNoCopy(out, "usage.total_tokens").Int(); gotTotalTokens != 22048 {
		t.Fatalf("expected total_tokens %d, got %d", 22048, gotTotalTokens)
	}
	if gotCachedTokens := ojson.GetBytesNoCopy(out, "usage.prompt_tokens_details.cached_tokens").Int(); gotCachedTokens != 22000 {
		t.Fatalf("expected cached_tokens %d, got %d", 22000, gotCachedTokens)
	}
	assertCachedCreationTokens(t, out, 31)
}

func TestConvertClaudeResponseToOpenAI_RefusalStopReason(t *testing.T) {
	testCases := []struct {
		name                string
		anthropicStopReason string
		wantFinishReason    string
	}{
		{
			name:                "refusal maps to content_filter",
			anthropicStopReason: "refusal",
			wantFinishReason:    "content_filter",
		},
		{
			name:                "sensitive maps to content_filter",
			anthropicStopReason: "sensitive",
			wantFinishReason:    "content_filter",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var param any

			out := ConvertClaudeResponseToOpenAI(
				ctx,
				"claude-opus-4-6",
				nil,
				nil,
				[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"`+tc.anthropicStopReason+`"},"usage":{"output_tokens":10}}`),
				&param,
			)
			if len(out) != 1 {
				t.Fatalf("expected 1 chunk, got %d", len(out))
			}

			gotFinishReason := ojson.GetBytesNoCopy(out[0], "choices.0.finish_reason").String()
			if gotFinishReason != tc.wantFinishReason {
				t.Fatalf("expected finish_reason %q, got %q, payload=%s", tc.wantFinishReason, gotFinishReason, string(out[0]))
			}
		})
	}
}

func TestConvertClaudeResponseToOpenAINonStream_RefusalStopReason(t *testing.T) {
	testCases := []struct {
		name                string
		anthropicStopReason string
		wantFinishReason    string
	}{
		{
			name:                "refusal maps to content_filter",
			anthropicStopReason: "refusal",
			wantFinishReason:    "content_filter",
		},
		{
			name:                "sensitive maps to content_filter",
			anthropicStopReason: "sensitive",
			wantFinishReason:    "content_filter",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			rawJSON := []byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"model\":\"claude-opus-4-6\"}}\n" +
				"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"" + tc.anthropicStopReason + "\"},\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}\n")

			out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "", nil, nil, rawJSON, nil)

			gotFinishReason := ojson.GetBytesNoCopy(out, "choices.0.finish_reason").String()
			if gotFinishReason != tc.wantFinishReason {
				t.Fatalf("expected finish_reason %q, got %q, payload=%s", tc.wantFinishReason, gotFinishReason, string(out))
			}
		})
	}
}

func TestConvertClaudeResponseToOpenAINonStream_ReasoningContent(t *testing.T) {
	rawJSON := []byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"model\":\"claude-opus-4-6\"}}\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"Let me analyze the problem.\"}}\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\" Step 2 is clear.\"}}\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n" +
		"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"Here is the solution.\"}}\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":1}\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}\n")

	out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "", nil, nil, rawJSON, nil)

	gotRC := ojson.GetBytesNoCopy(out, "choices.0.message.reasoning_content")
	if !gotRC.Exists() {
		t.Fatalf("expected choices.0.message.reasoning_content to exist, payload=%s", string(out))
	}
	wantRC := "Let me analyze the problem. Step 2 is clear."
	if gotRC.String() != wantRC {
		t.Fatalf("reasoning_content = %q, want %q", gotRC.String(), wantRC)
	}

	if gotOldReasoning := ojson.GetBytesNoCopy(out, "choices.0.message.reasoning"); gotOldReasoning.Exists() {
		t.Fatalf("choices.0.message.reasoning should not exist, got %q", gotOldReasoning.String())
	}

	gotContent := ojson.GetBytesNoCopy(out, "choices.0.message.content").String()
	wantContent := "Here is the solution."
	if gotContent != wantContent {
		t.Fatalf("content = %q, want %q", gotContent, wantContent)
	}
}

func TestConvertClaudeResponseToOpenAINonStream_OmitsReasoningContentWhenAbsent(t *testing.T) {
	rawJSON := []byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"model\":\"claude-opus-4-6\"}}\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Just plain text.\"}}\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}\n")

	out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "", nil, nil, rawJSON, nil)

	if gotRC := ojson.GetBytesNoCopy(out, "choices.0.message.reasoning_content"); gotRC.Exists() {
		t.Fatalf("choices.0.message.reasoning_content should be omitted when absent, got %q", gotRC.String())
	}
	if gotReasoning := ojson.GetBytesNoCopy(out, "choices.0.message.reasoning"); gotReasoning.Exists() {
		t.Fatalf("choices.0.message.reasoning should not exist, got %q", gotReasoning.String())
	}
	if gotContent := ojson.GetBytesNoCopy(out, "choices.0.message.content").String(); gotContent != "Just plain text." {
		t.Fatalf("content = %q, want %q", gotContent, "Just plain text.")
	}
}

func TestConvertClaudeResponseToOpenAI_StreamAndNonStreamParity(t *testing.T) {
	events := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","model":"claude-opus-4-6","usage":{"input_tokens":15,"output_tokens":1}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"First thought. "}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Second thought."}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Final "}}`),
		[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"answer."}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":25}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	// 1. Process via streaming
	ctx := context.Background()
	var param any
	var streamReasoning string
	var streamContent string
	var streamFinishReason string

	for _, ev := range events {
		chunks := ConvertClaudeResponseToOpenAI(ctx, "claude-opus-4-6", nil, nil, ev, &param)
		for _, chunk := range chunks {
			if rc := ojson.GetBytesNoCopy(chunk, "choices.0.delta.reasoning_content"); rc.Exists() {
				streamReasoning += rc.String()
			}
			if c := ojson.GetBytesNoCopy(chunk, "choices.0.delta.content"); c.Exists() {
				streamContent += c.String()
			}
			if fr := ojson.GetBytesNoCopy(chunk, "choices.0.finish_reason"); fr.Exists() && fr.String() != "" {
				streamFinishReason = fr.String()
			}
		}
	}

	// 2. Process via non-stream
	var rawBuffer []byte
	for _, ev := range events {
		rawBuffer = append(rawBuffer, ev...)
		rawBuffer = append(rawBuffer, '\n')
	}

	nonStreamOut := ConvertClaudeResponseToOpenAINonStream(ctx, "", nil, nil, rawBuffer, nil)
	nonStreamRC := ojson.GetBytesNoCopy(nonStreamOut, "choices.0.message.reasoning_content").String()
	nonStreamContent := ojson.GetBytesNoCopy(nonStreamOut, "choices.0.message.content").String()
	nonStreamFinishReason := ojson.GetBytesNoCopy(nonStreamOut, "choices.0.finish_reason").String()

	if streamReasoning != "First thought. Second thought." {
		t.Fatalf("streamReasoning = %q, want %q", streamReasoning, "First thought. Second thought.")
	}
	if nonStreamRC != streamReasoning {
		t.Fatalf("parity mismatch for reasoning_content: nonStream=%q, stream=%q", nonStreamRC, streamReasoning)
	}
	if streamContent != "Final answer." {
		t.Fatalf("streamContent = %q, want %q", streamContent, "Final answer.")
	}
	if nonStreamContent != streamContent {
		t.Fatalf("parity mismatch for content: nonStream=%q, stream=%q", nonStreamContent, streamContent)
	}
	if streamFinishReason != "stop" {
		t.Fatalf("streamFinishReason = %q, want %q", streamFinishReason, "stop")
	}
	if nonStreamFinishReason != streamFinishReason {
		t.Fatalf("parity mismatch for finish_reason: nonStream=%q, stream=%q", nonStreamFinishReason, streamFinishReason)
	}
}

func TestConvertClaudeResponseToOpenAI_RedactedThinkingIgnored(t *testing.T) {
	events := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","model":"claude-opus-4-6"}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"encrypted_blob"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Visible reply."}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":10,"output_tokens":20}}`),
	}

	// Non-stream check
	var rawJSON []byte
	for _, ev := range events {
		rawJSON = append(rawJSON, ev...)
		rawJSON = append(rawJSON, '\n')
	}

	outNonStream := ConvertClaudeResponseToOpenAINonStream(context.Background(), "", nil, nil, rawJSON, nil)
	if gotRC := ojson.GetBytesNoCopy(outNonStream, "choices.0.message.reasoning_content"); gotRC.Exists() {
		t.Fatalf("redacted_thinking must never map to reasoning_content in non-stream, got %q", gotRC.String())
	}
	if gotReasoning := ojson.GetBytesNoCopy(outNonStream, "choices.0.message.reasoning"); gotReasoning.Exists() {
		t.Fatalf("redacted_thinking must not produce reasoning field in non-stream, got %q", gotReasoning.String())
	}
	if gotContent := ojson.GetBytesNoCopy(outNonStream, "choices.0.message.content").String(); gotContent != "Visible reply." {
		t.Fatalf("content = %q, want %q", gotContent, "Visible reply.")
	}

	// Stream check
	ctx := context.Background()
	var param any
	var streamContent string
	for _, line := range events {
		chunks := ConvertClaudeResponseToOpenAI(ctx, "claude-opus-4-6", nil, nil, line, &param)
		for _, chunk := range chunks {
			if gotRC := ojson.GetBytesNoCopy(chunk, "choices.0.delta.reasoning_content"); gotRC.Exists() {
				t.Fatalf("redacted_thinking must never map to reasoning_content in stream, got %q", gotRC.String())
			}
			if gotReasoning := ojson.GetBytesNoCopy(chunk, "choices.0.delta.reasoning"); gotReasoning.Exists() {
				t.Fatalf("redacted_thinking must not produce delta.reasoning field in stream, got %q", gotReasoning.String())
			}
			if c := ojson.GetBytesNoCopy(chunk, "choices.0.delta.content"); c.Exists() {
				streamContent += c.String()
			}
		}
	}
	if streamContent != "Visible reply." {
		t.Fatalf("stream content = %q, want %q", streamContent, "Visible reply.")
	}
}

func TestConvertClaudeResponseToOpenAI_StreamToolCallIndexIsZeroBased(t *testing.T) {
	events := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","model":"claude-opus-4-6","usage":{"input_tokens":15,"output_tokens":1}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Thinking..."}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather"}}`),
		[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\": \"Paris\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_2","name":"get_time"}}`),
		[]byte(`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Tokyo\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":2}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":25}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	ctx := context.Background()
	var param any
	type toolCallInfo struct {
		Index     int64
		ID        string
		Name      string
		Arguments string
	}
	var streamedCalls []toolCallInfo

	for _, ev := range events {
		chunks := ConvertClaudeResponseToOpenAI(ctx, "claude-opus-4-6", nil, nil, ev, &param)
		for _, chunk := range chunks {
			tc := ojson.GetBytesNoCopy(chunk, "choices.0.delta.tool_calls.0")
			if tc.Exists() {
				streamedCalls = append(streamedCalls, toolCallInfo{
					Index:     tc.Get("index").Int(),
					ID:        tc.Get("id").String(),
					Name:      tc.Get("function.name").String(),
					Arguments: tc.Get("function.arguments").String(),
				})
			}
		}
	}

	if len(streamedCalls) != 2 {
		t.Fatalf("expected 2 streamed tool calls, got %d", len(streamedCalls))
	}

	if streamedCalls[0].Index != 0 {
		t.Errorf("tool call toolu_1 index = %d, want 0", streamedCalls[0].Index)
	}
	if streamedCalls[0].ID != "toolu_1" || streamedCalls[0].Name != "get_weather" || streamedCalls[0].Arguments != `{"city": "Paris"}` {
		t.Errorf("tool call toolu_1 payload mismatch: %+v", streamedCalls[0])
	}

	if streamedCalls[1].Index != 1 {
		t.Errorf("tool call toolu_2 index = %d, want 1", streamedCalls[1].Index)
	}
	if streamedCalls[1].ID != "toolu_2" || streamedCalls[1].Name != "get_time" || streamedCalls[1].Arguments != `{"city":"Tokyo"}` {
		t.Errorf("tool call toolu_2 payload mismatch: %+v", streamedCalls[1])
	}
}

func TestConvertClaudeResponseToOpenAI_StreamEmitsTrailingUsageChunkWithCacheDetails(t *testing.T) {
	ctx := context.Background()
	var param any

	events := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","model":"claude-opus-4-6","usage":{"input_tokens":100,"cache_creation_input_tokens":20,"cache_read_input_tokens":50,"output_tokens":1}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":15}}`),
		[]byte(`data: {"type":"message_stop"}`),
		[]byte(`data: {"type":"message_stop"}`), // Duplicate message_stop should be ignored via TrailingUsageSent
	}

	var allChunks [][]byte
	for _, ev := range events {
		chunks := ConvertClaudeResponseToOpenAI(ctx, "claude-opus-4-6", nil, nil, ev, &param)
		allChunks = append(allChunks, chunks...)
	}

	// Verify exactly one trailing usage chunk with empty choices array exists as expected by OpenAI streaming spec and LiteLLM
	var trailingUsageChunk []byte
	var trailingUsageChunkIndex = -1
	var finishReasonChunkIndex = -1
	trailingUsageChunkCount := 0

	for i, chunk := range allChunks {
		if ojson.GetBytesNoCopy(chunk, "choices.0.finish_reason").Exists() && ojson.GetBytesNoCopy(chunk, "choices.0.finish_reason").String() != "" {
			finishReasonChunkIndex = i
		}
		choices := ojson.GetBytesNoCopy(chunk, "choices")
		if choices.Exists() && len(choices.Array()) == 0 && ojson.GetBytesNoCopy(chunk, "usage").Exists() {
			trailingUsageChunk = chunk
			trailingUsageChunkIndex = i
			trailingUsageChunkCount++
		}
	}

	if trailingUsageChunkCount != 1 {
		t.Fatalf("expected exactly 1 trailing usage chunk with empty choices array (choices: []), got %d; chunks: %s", trailingUsageChunkCount, string(bytes.Join(allChunks, []byte("\n"))))
	}

	if finishReasonChunkIndex >= trailingUsageChunkIndex {
		t.Fatalf("expected finish_reason chunk (index %d) before trailing usage chunk (index %d)", finishReasonChunkIndex, trailingUsageChunkIndex)
	}

	if gotPromptTokens := ojson.GetBytesNoCopy(trailingUsageChunk, "usage.prompt_tokens").Int(); gotPromptTokens != 170 {
		t.Errorf("expected prompt_tokens 170, got %d", gotPromptTokens)
	}
	if gotCompletionTokens := ojson.GetBytesNoCopy(trailingUsageChunk, "usage.completion_tokens").Int(); gotCompletionTokens != 15 {
		t.Errorf("expected completion_tokens 15, got %d", gotCompletionTokens)
	}
	if gotTotalTokens := ojson.GetBytesNoCopy(trailingUsageChunk, "usage.total_tokens").Int(); gotTotalTokens != 185 {
		t.Errorf("expected total_tokens 185, got %d", gotTotalTokens)
	}
	if gotCachedTokens := ojson.GetBytesNoCopy(trailingUsageChunk, "usage.prompt_tokens_details.cached_tokens").Int(); gotCachedTokens != 50 {
		t.Errorf("expected cached_tokens 50, got %d", gotCachedTokens)
	}
	if gotCacheWriteTokens := ojson.GetBytesNoCopy(trailingUsageChunk, "usage.prompt_tokens_details.cache_write_tokens").Int(); gotCacheWriteTokens != 20 {
		t.Errorf("expected cache_write_tokens 20, got %d", gotCacheWriteTokens)
	}
	if gotCachedCreationTokens := ojson.GetBytesNoCopy(trailingUsageChunk, "usage.prompt_tokens_details.cached_creation_tokens").Int(); gotCachedCreationTokens != 20 {
		t.Errorf("expected cached_creation_tokens 20, got %d", gotCachedCreationTokens)
	}
}

func assertReasoningTokens(t *testing.T, payload []byte, want int64) {
	t.Helper()

	got := ojson.GetBytesNoCopy(payload, "usage.completion_tokens_details.reasoning_tokens")
	if !got.Exists() {
		t.Fatalf("expected reasoning_tokens to exist, payload=%s", string(payload))
	}
	if got.Int() != want {
		t.Fatalf("expected reasoning_tokens %d, got %d", want, got.Int())
	}
}

func TestConvertClaudeResponseToOpenAI_StreamUsageIncludesReasoningTokens(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertClaudeResponseToOpenAI(
		ctx,
		"claude-sonnet-5",
		nil,
		nil,
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":10,"output_tokens":38,"output_tokens_details":{"thinking_tokens":23}}}`),
		&param,
	)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}
	if gotCompletionTokens := ojson.GetBytesNoCopy(out[0], "usage.completion_tokens").Int(); gotCompletionTokens != 38 {
		t.Fatalf("expected completion_tokens %d, got %d", 38, gotCompletionTokens)
	}
	assertReasoningTokens(t, out[0], 23)
}

func TestConvertClaudeResponseToOpenAI_StreamTrailingUsageIncludesReasoningTokens(t *testing.T) {
	ctx := context.Background()
	var param any

	events := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","model":"claude-sonnet-5","usage":{"input_tokens":10,"output_tokens":1}}}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":38,"output_tokens_details":{"thinking_tokens":23}}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var trailing [][]byte
	for _, ev := range events {
		for _, chunk := range ConvertClaudeResponseToOpenAI(ctx, "claude-sonnet-5", nil, nil, ev, &param) {
			choices := ojson.GetBytesNoCopy(chunk, "choices")
			if choices.Exists() && len(choices.Array()) == 0 && ojson.GetBytesNoCopy(chunk, "usage").Exists() {
				trailing = append(trailing, chunk)
			}
		}
	}
	if len(trailing) != 1 {
		t.Fatalf("expected 1 trailing usage chunk, got %d", len(trailing))
	}
	assertReasoningTokens(t, trailing[0], 23)
}

func TestConvertClaudeResponseToOpenAINonStream_UsageIncludesReasoningTokens(t *testing.T) {
	rawJSON := []byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":52,"output_tokens_details":{"thinking_tokens":32}}}`)

	out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "", nil, nil, rawJSON, nil)

	if gotCompletionTokens := ojson.GetBytesNoCopy(out, "usage.completion_tokens").Int(); gotCompletionTokens != 52 {
		t.Fatalf("expected completion_tokens %d, got %d", 52, gotCompletionTokens)
	}
	assertReasoningTokens(t, out, 32)
}

func TestConvertClaudeResponseToOpenAI_OmitsReasoningTokensWhenNotReported(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertClaudeResponseToOpenAI(
		ctx,
		"claude-sonnet-5",
		nil,
		nil,
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":10,"output_tokens":38}}`),
		&param,
	)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}
	if got := ojson.GetBytesNoCopy(out[0], "usage.completion_tokens_details.reasoning_tokens"); got.Exists() {
		t.Fatalf("expected reasoning_tokens to be omitted, got %s; payload=%s", got.Raw, string(out[0]))
	}

	nonStream := ConvertClaudeResponseToOpenAINonStream(ctx, "", nil, nil, []byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"model\":\"claude-sonnet-5\"}}\n"+"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":10,\"output_tokens\":38}}\n"), nil)
	if got := ojson.GetBytesNoCopy(nonStream, "usage.completion_tokens_details.reasoning_tokens"); got.Exists() {
		t.Fatalf("expected reasoning_tokens to be omitted, got %s; payload=%s", got.Raw, string(nonStream))
	}
}

func TestConvertClaudeResponseToOpenAI_ReportsZeroReasoningTokensWhenThinkingIsZero(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertClaudeResponseToOpenAI(
		ctx,
		"claude-sonnet-5",
		nil,
		nil,
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":10,"output_tokens":38,"output_tokens_details":{"thinking_tokens":0}}}`),
		&param,
	)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}
	assertReasoningTokens(t, out[0], 0)
}

func TestConvertClaudeResponseToOpenAI_StreamPreservesUpstreamServingModel(t *testing.T) {
	ctx := context.Background()
	var param any

	events := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_fb_1","model":"claude-opus-4-8","usage":{"input_tokens":10,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"fallback answer"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4,"iterations":[{"type":"message","model":"claude-opus-5-5","output_tokens":0},{"type":"fallback_message","model":"claude-opus-4-8","output_tokens":4}]}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var chunks [][]byte
	for _, ev := range events {
		chunks = append(chunks, ConvertClaudeResponseToOpenAI(ctx, "claude-opus-5-5", nil, nil, ev, &param)...)
	}
	if len(chunks) == 0 {
		t.Fatal("expected stream chunks")
	}
	sawFallbackDelta := false
	for i, chunk := range chunks {
		if got := ojson.GetBytesNoCopy(chunk, "model").String(); got != "claude-opus-4-8" {
			t.Fatalf("chunk[%d] model = %q, want claude-opus-4-8; payload=%s", i, got, string(chunk))
		}
		if fb := ojson.GetBytesNoCopy(chunk, "choices.0.delta.fallback"); fb.Exists() {
			sawFallbackDelta = true
			if fb.Get("from.model").String() != "claude-opus-5-5" || fb.Get("to.model").String() != "claude-opus-4-8" {
				t.Fatalf("chunk[%d] delta.fallback = %s, want from claude-opus-5-5 to claude-opus-4-8", i, fb.Raw)
			}
		}
	}
	if !sawFallbackDelta {
		t.Fatal("expected pre-token fallback stream to surface choices[0].delta.fallback")
	}

	// Also verify when message_start already reports claude-opus-4-8 AND a fallback content_block is emitted.
	var paramPreBlock any
	preBlockEvents := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_fb_preblock","model":"claude-opus-4-8","usage":{"input_tokens":10,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"fallback","from":{"model":"claude-opus-5-5"},"to":{"model":"claude-opus-4-8"},"trigger":{"type":"refusal","category":"reasoning_extraction"}}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"ok"}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}
	sawTriggerChunk := false
	for _, ev := range preBlockEvents {
		for _, chunk := range ConvertClaudeResponseToOpenAI(ctx, "claude-opus-5-5", nil, nil, ev, &paramPreBlock) {
			if fb := ojson.GetBytesNoCopy(chunk, "choices.0.delta.fallback"); fb.Exists() && fb.Get("trigger.category").String() == "reasoning_extraction" {
				sawTriggerChunk = true
			}
		}
	}
	if !sawTriggerChunk {
		t.Fatal("expected fallback content_block to surface trigger in choices[0].delta.fallback even when message_start already reported target model")
	}

	// Pre-token non-stream response via iterations also surfaces choices[0].message.fallback with content_offset=0.
	preNonStreamRaw := []byte(`{"id":"msg_fb_1","type":"message","role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"fallback answer"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":4,"iterations":[{"type":"message","model":"claude-opus-5-5","output_tokens":0},{"type":"fallback_message","model":"claude-opus-4-8","output_tokens":4}]}}`)
	preNonStreamOut := ConvertClaudeResponseToOpenAINonStream(ctx, "claude-opus-5-5", nil, nil, preNonStreamRaw, nil)
	preFB := ojson.GetBytesNoCopy(preNonStreamOut, "choices.0.message.fallback")
	if !preFB.Exists() || preFB.Get("from.model").String() != "claude-opus-5-5" || preFB.Get("to.model").String() != "claude-opus-4-8" || preFB.Get("content_offset").Int() != 0 {
		t.Fatalf("pre-token non-stream choices[0].message.fallback = %s, want from=5-5 to=4-8 content_offset=0", preFB.Raw)
	}
}

func TestConvertClaudeResponseToOpenAI_MidStreamFallbackUpdatesServingModelAndEmitsBoundary(t *testing.T) {
	ctx := context.Background()
	var param any

	events := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_fb_mid","model":"claude-opus-5-5","usage":{"input_tokens":10,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial "}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"fallback","from":{"model":"claude-opus-5-5"},"to":{"model":"claude-opus-4-8"},"trigger":{"type":"refusal","category":"cyber"}}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"fallback text"}}`),
		[]byte(`data: {"type":"content_block_stop","index":2}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":6,"iterations":[{"type":"message","model":"claude-opus-5-5","output_tokens":2},{"type":"fallback_message","model":"claude-opus-4-8","output_tokens":4}]}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var models []string
	var rawChunks [][]byte
	var content string
	for _, ev := range events {
		for _, chunk := range ConvertClaudeResponseToOpenAI(ctx, "claude-opus-5-5", nil, nil, ev, &param) {
			rawChunks = append(rawChunks, chunk)
			models = append(models, ojson.GetBytesNoCopy(chunk, "model").String())
			if deltaText := ojson.GetBytesNoCopy(chunk, "choices.0.delta.content"); deltaText.Exists() {
				content += deltaText.String()
			}
		}
	}

	// Expected sequence: message_start (5.5), partial delta (5.5), fallback boundary (4.8), fallback delta (4.8), message_delta (4.8), trailing usage (4.8).
	wantModels := []string{
		"claude-opus-5-5",
		"claude-opus-5-5",
		"claude-opus-4-8",
		"claude-opus-4-8",
		"claude-opus-4-8",
		"claude-opus-4-8",
	}
	if len(models) != len(wantModels) {
		t.Fatalf("chunk models = %v (len %d), want %v (len %d)", models, len(models), wantModels, len(wantModels))
	}
	for i := range wantModels {
		if models[i] != wantModels[i] {
			t.Fatalf("chunk[%d] model = %q, want %q (all=%v)", i, models[i], wantModels[i], models)
		}
	}
	boundaryFB := ojson.GetBytesNoCopy(rawChunks[2], "choices.0.delta.fallback")
	if !boundaryFB.Exists() || boundaryFB.Get("from.model").String() != "claude-opus-5-5" || boundaryFB.Get("to.model").String() != "claude-opus-4-8" || boundaryFB.Get("trigger.category").String() != "cyber" {
		t.Fatalf("boundary chunk[2] delta.fallback = %s, want from=claude-opus-5-5 to=claude-opus-4-8 trigger.category=cyber", boundaryFB.Raw)
	}
	if content != "partial fallback text" {
		t.Fatalf("accumulated content = %q, want %q", content, "partial fallback text")
	}

	nonStreamRaw := []byte(`{"id":"msg_fb_mid","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"partial "},{"type":"fallback","from":{"model":"claude-opus-5-5"},"to":{"model":"claude-opus-4-8"},"trigger":{"type":"refusal","category":"cyber"}},{"type":"text","text":"fallback text"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":6,"iterations":[{"type":"message","model":"claude-opus-5-5","output_tokens":2},{"type":"fallback_message","model":"claude-opus-4-8","output_tokens":4}]}}`)
	nonStreamOut := ConvertClaudeResponseToOpenAINonStream(ctx, "claude-opus-5-5", nil, nil, nonStreamRaw, nil)
	if got := ojson.GetBytesNoCopy(nonStreamOut, "model").String(); got != "claude-opus-4-8" {
		t.Fatalf("non-stream model = %q, want claude-opus-4-8; payload=%s", got, string(nonStreamOut))
	}
	if gotContent := ojson.GetBytesNoCopy(nonStreamOut, "choices.0.message.content").String(); gotContent != "partial fallback text" {
		t.Fatalf("non-stream content = %q, want %q", gotContent, "partial fallback text")
	}
	msgFB := ojson.GetBytesNoCopy(nonStreamOut, "choices.0.message.fallback")
	if !msgFB.Exists() || msgFB.Get("from.model").String() != "claude-opus-5-5" || msgFB.Get("to.model").String() != "claude-opus-4-8" || msgFB.Get("trigger.category").String() != "cyber" || msgFB.Get("content_offset").Int() != int64(len("partial ")) {
		t.Fatalf("non-stream choices[0].message.fallback = %s, want from=5-5 to=4-8 trigger=cyber content_offset=8", msgFB.Raw)
	}
}

func TestConvertClaudeResponseToOpenAI_MultiHopFallbackAndStickyDefault(t *testing.T) {
	ctx := context.Background()

	// 1. Multi-hop chain: claude-fable-5-1 ("partA ") -> claude-opus-5 ("partB ") -> claude-opus-4-8 ("partC").
	rawJSON := []byte(`{"id":"msg_chat_chain","type":"message","role":"assistant","model":"claude-fable-5-1","content":[{"type":"text","text":"partA "},{"type":"fallback","from":{"model":"claude-fable-5-1"},"to":{"model":"claude-opus-5"},"trigger":{"type":"refusal","category":"reasoning_extraction"}},{"type":"text","text":"partB "},{"type":"fallback","from":{"model":"claude-opus-5"},"to":{"model":"claude-opus-4-8"},"trigger":{"type":"refusal","category":"cyber"}},{"type":"text","text":"partC"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":9}}`)
	out := ConvertClaudeResponseToOpenAINonStream(ctx, "claude-fable-5-1", nil, nil, rawJSON, nil)
	if got := ojson.GetBytesNoCopy(out, "model").String(); got != "claude-opus-4-8" {
		t.Fatalf("multi-hop Chat model = %q, want claude-opus-4-8", got)
	}
	hops := ojson.GetBytesNoCopy(out, "choices.0.message.fallbacks").Array()
	if len(hops) != 2 {
		t.Fatalf("multi-hop Chat choices[0].message.fallbacks len = %d, want 2; payload=%s", len(hops), string(out))
	}
	if hops[0].Get("from.model").String() != "claude-fable-5-1" || hops[0].Get("to.model").String() != "claude-opus-5" || hops[0].Get("content_offset").Int() != 6 {
		t.Fatalf("Chat hop[0] = %s, want fable-5-1 -> opus-5 at content_offset=6", hops[0].Raw)
	}
	if hops[1].Get("from.model").String() != "claude-opus-5" || hops[1].Get("to.model").String() != "claude-opus-4-8" || hops[1].Get("content_offset").Int() != 12 {
		t.Fatalf("Chat hop[1] = %s, want opus-5 -> opus-4-8 at content_offset=12", hops[1].Raw)
	}

	// 2. Sticky routing with "fallbacks": "default" (no fallback block, no usage.iterations).
	translatedDefaultReq := []byte(`{"model":"claude-opus-5-5","fallbacks":"default","messages":[{"role":"user","content":"turn 2"}]}`)
	var stickyParam any
	stickyStart := []byte(`data: {"type":"message_start","message":{"id":"msg_sticky_chat","model":"claude-opus-4-8","usage":{"input_tokens":5,"output_tokens":0}}}`)
	stickyChunks := ConvertClaudeResponseToOpenAI(ctx, "claude-opus-5-5", nil, translatedDefaultReq, stickyStart, &stickyParam)
	if len(stickyChunks) != 1 || ojson.GetBytesNoCopy(stickyChunks[0], "choices.0.delta.fallback.to.model").String() != "claude-opus-4-8" {
		t.Fatalf("sticky default Chat start chunk = %s, want delta.fallback.to.model=claude-opus-4-8", string(stickyChunks[0]))
	}
}
