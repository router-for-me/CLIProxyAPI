package chat_completions

import (
	"bytes"
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func assertCachedCreationTokens(t *testing.T, payload []byte, want int64) {
	t.Helper()

	got := gjson.GetBytes(payload, "usage.prompt_tokens_details.cached_creation_tokens")
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

	if gotPromptTokens := gjson.GetBytes(out[0], "usage.prompt_tokens").Int(); gotPromptTokens != 22044 {
		t.Fatalf("expected prompt_tokens %d, got %d", 22044, gotPromptTokens)
	}
	if gotCompletionTokens := gjson.GetBytes(out[0], "usage.completion_tokens").Int(); gotCompletionTokens != 4 {
		t.Fatalf("expected completion_tokens %d, got %d", 4, gotCompletionTokens)
	}
	if gotTotalTokens := gjson.GetBytes(out[0], "usage.total_tokens").Int(); gotTotalTokens != 22048 {
		t.Fatalf("expected total_tokens %d, got %d", 22048, gotTotalTokens)
	}
	if gotCachedTokens := gjson.GetBytes(out[0], "usage.prompt_tokens_details.cached_tokens").Int(); gotCachedTokens != 22000 {
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

	if gotPromptTokens := gjson.GetBytes(out[0], "usage.prompt_tokens").Int(); gotPromptTokens != 22044 {
		t.Fatalf("expected prompt_tokens %d, got %d", 22044, gotPromptTokens)
	}
	if gotCompletionTokens := gjson.GetBytes(out[0], "usage.completion_tokens").Int(); gotCompletionTokens != 4 {
		t.Fatalf("expected completion_tokens %d, got %d", 4, gotCompletionTokens)
	}
	if gotTotalTokens := gjson.GetBytes(out[0], "usage.total_tokens").Int(); gotTotalTokens != 22048 {
		t.Fatalf("expected total_tokens %d, got %d", 22048, gotTotalTokens)
	}
	if gotCachedTokens := gjson.GetBytes(out[0], "usage.prompt_tokens_details.cached_tokens").Int(); gotCachedTokens != 22000 {
		t.Fatalf("expected cached_tokens %d, got %d", 22000, gotCachedTokens)
	}
	assertCachedCreationTokens(t, out[0], 31)
}

func TestConvertClaudeResponseToOpenAINonStream_UsageIncludesCachedTokens(t *testing.T) {
	rawJSON := []byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"model\":\"claude-opus-4-6\"}}\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":13,\"output_tokens\":4,\"cache_read_input_tokens\":22000,\"cache_creation_input_tokens\":31}}\n")

	out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "", nil, nil, rawJSON, nil)

	if gotPromptTokens := gjson.GetBytes(out, "usage.prompt_tokens").Int(); gotPromptTokens != 22044 {
		t.Fatalf("expected prompt_tokens %d, got %d", 22044, gotPromptTokens)
	}
	if gotCompletionTokens := gjson.GetBytes(out, "usage.completion_tokens").Int(); gotCompletionTokens != 4 {
		t.Fatalf("expected completion_tokens %d, got %d", 4, gotCompletionTokens)
	}
	if gotTotalTokens := gjson.GetBytes(out, "usage.total_tokens").Int(); gotTotalTokens != 22048 {
		t.Fatalf("expected total_tokens %d, got %d", 22048, gotTotalTokens)
	}
	if gotCachedTokens := gjson.GetBytes(out, "usage.prompt_tokens_details.cached_tokens").Int(); gotCachedTokens != 22000 {
		t.Fatalf("expected cached_tokens %d, got %d", 22000, gotCachedTokens)
	}
	assertCachedCreationTokens(t, out, 31)
}

func TestConvertClaudeResponseToOpenAINonStream_UsageMergesMessageStartUsage(t *testing.T) {
	rawJSON := []byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"model\":\"claude-opus-4-6\",\"usage\":{\"input_tokens\":13,\"output_tokens\":1,\"cache_read_input_tokens\":22000,\"cache_creation_input_tokens\":31}}}\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":4}}\n")

	out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "", nil, nil, rawJSON, nil)

	if gotPromptTokens := gjson.GetBytes(out, "usage.prompt_tokens").Int(); gotPromptTokens != 22044 {
		t.Fatalf("expected prompt_tokens %d, got %d", 22044, gotPromptTokens)
	}
	if gotCompletionTokens := gjson.GetBytes(out, "usage.completion_tokens").Int(); gotCompletionTokens != 4 {
		t.Fatalf("expected completion_tokens %d, got %d", 4, gotCompletionTokens)
	}
	if gotTotalTokens := gjson.GetBytes(out, "usage.total_tokens").Int(); gotTotalTokens != 22048 {
		t.Fatalf("expected total_tokens %d, got %d", 22048, gotTotalTokens)
	}
	if gotCachedTokens := gjson.GetBytes(out, "usage.prompt_tokens_details.cached_tokens").Int(); gotCachedTokens != 22000 {
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

			gotFinishReason := gjson.GetBytes(out[0], "choices.0.finish_reason").String()
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

			gotFinishReason := gjson.GetBytes(out, "choices.0.finish_reason").String()
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

	gotRC := gjson.GetBytes(out, "choices.0.message.reasoning_content")
	if !gotRC.Exists() {
		t.Fatalf("expected choices.0.message.reasoning_content to exist, payload=%s", string(out))
	}
	wantRC := "Let me analyze the problem. Step 2 is clear."
	if gotRC.String() != wantRC {
		t.Fatalf("reasoning_content = %q, want %q", gotRC.String(), wantRC)
	}

	if gotOldReasoning := gjson.GetBytes(out, "choices.0.message.reasoning"); gotOldReasoning.Exists() {
		t.Fatalf("choices.0.message.reasoning should not exist, got %q", gotOldReasoning.String())
	}

	gotContent := gjson.GetBytes(out, "choices.0.message.content").String()
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

	if gotRC := gjson.GetBytes(out, "choices.0.message.reasoning_content"); gotRC.Exists() {
		t.Fatalf("choices.0.message.reasoning_content should be omitted when absent, got %q", gotRC.String())
	}
	if gotReasoning := gjson.GetBytes(out, "choices.0.message.reasoning"); gotReasoning.Exists() {
		t.Fatalf("choices.0.message.reasoning should not exist, got %q", gotReasoning.String())
	}
	if gotContent := gjson.GetBytes(out, "choices.0.message.content").String(); gotContent != "Just plain text." {
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
			if rc := gjson.GetBytes(chunk, "choices.0.delta.reasoning_content"); rc.Exists() {
				streamReasoning += rc.String()
			}
			if c := gjson.GetBytes(chunk, "choices.0.delta.content"); c.Exists() {
				streamContent += c.String()
			}
			if fr := gjson.GetBytes(chunk, "choices.0.finish_reason"); fr.Exists() && fr.String() != "" {
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
	nonStreamRC := gjson.GetBytes(nonStreamOut, "choices.0.message.reasoning_content").String()
	nonStreamContent := gjson.GetBytes(nonStreamOut, "choices.0.message.content").String()
	nonStreamFinishReason := gjson.GetBytes(nonStreamOut, "choices.0.finish_reason").String()

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
	if gotRC := gjson.GetBytes(outNonStream, "choices.0.message.reasoning_content"); gotRC.Exists() {
		t.Fatalf("redacted_thinking must never map to reasoning_content in non-stream, got %q", gotRC.String())
	}
	if gotReasoning := gjson.GetBytes(outNonStream, "choices.0.message.reasoning"); gotReasoning.Exists() {
		t.Fatalf("redacted_thinking must not produce reasoning field in non-stream, got %q", gotReasoning.String())
	}
	if gotContent := gjson.GetBytes(outNonStream, "choices.0.message.content").String(); gotContent != "Visible reply." {
		t.Fatalf("content = %q, want %q", gotContent, "Visible reply.")
	}

	// Stream check
	ctx := context.Background()
	var param any
	var streamContent string
	for _, line := range events {
		chunks := ConvertClaudeResponseToOpenAI(ctx, "claude-opus-4-6", nil, nil, line, &param)
		for _, chunk := range chunks {
			if gotRC := gjson.GetBytes(chunk, "choices.0.delta.reasoning_content"); gotRC.Exists() {
				t.Fatalf("redacted_thinking must never map to reasoning_content in stream, got %q", gotRC.String())
			}
			if gotReasoning := gjson.GetBytes(chunk, "choices.0.delta.reasoning"); gotReasoning.Exists() {
				t.Fatalf("redacted_thinking must not produce delta.reasoning field in stream, got %q", gotReasoning.String())
			}
			if c := gjson.GetBytes(chunk, "choices.0.delta.content"); c.Exists() {
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
			tc := gjson.GetBytes(chunk, "choices.0.delta.tool_calls.0")
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
		if gjson.GetBytes(chunk, "choices.0.finish_reason").Exists() && gjson.GetBytes(chunk, "choices.0.finish_reason").String() != "" {
			finishReasonChunkIndex = i
		}
		choices := gjson.GetBytes(chunk, "choices")
		if choices.Exists() && len(choices.Array()) == 0 && gjson.GetBytes(chunk, "usage").Exists() {
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

	if gotPromptTokens := gjson.GetBytes(trailingUsageChunk, "usage.prompt_tokens").Int(); gotPromptTokens != 170 {
		t.Errorf("expected prompt_tokens 170, got %d", gotPromptTokens)
	}
	if gotCompletionTokens := gjson.GetBytes(trailingUsageChunk, "usage.completion_tokens").Int(); gotCompletionTokens != 15 {
		t.Errorf("expected completion_tokens 15, got %d", gotCompletionTokens)
	}
	if gotTotalTokens := gjson.GetBytes(trailingUsageChunk, "usage.total_tokens").Int(); gotTotalTokens != 185 {
		t.Errorf("expected total_tokens 185, got %d", gotTotalTokens)
	}
	if gotCachedTokens := gjson.GetBytes(trailingUsageChunk, "usage.prompt_tokens_details.cached_tokens").Int(); gotCachedTokens != 50 {
		t.Errorf("expected cached_tokens 50, got %d", gotCachedTokens)
	}
	if gotCacheWriteTokens := gjson.GetBytes(trailingUsageChunk, "usage.prompt_tokens_details.cache_write_tokens").Int(); gotCacheWriteTokens != 20 {
		t.Errorf("expected cache_write_tokens 20, got %d", gotCacheWriteTokens)
	}
	if gotCachedCreationTokens := gjson.GetBytes(trailingUsageChunk, "usage.prompt_tokens_details.cached_creation_tokens").Int(); gotCachedCreationTokens != 20 {
		t.Errorf("expected cached_creation_tokens 20, got %d", gotCachedCreationTokens)
	}
}
