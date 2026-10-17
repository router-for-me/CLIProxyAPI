package chat_completions

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
)

func TestFinishReasonToolCallsNotOverwritten(t *testing.T) {
	ctx := context.Background()
	var param any

	// Chunk 1: Contains functionCall - should set SawToolCall = true
	chunk1 := []byte(`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"list_files","args":{"path":"."}}}]}}]}}`)
	result1 := ConvertAntigravityResponseToOpenAI(ctx, "model", nil, nil, chunk1, &param)

	// Verify chunk1 has no finish_reason (null)
	if len(result1) != 1 {
		t.Fatalf("Expected 1 result from chunk1, got %d", len(result1))
	}
	fr1 := gjson.GetBytes(result1[0], "choices.0.finish_reason")
	if fr1.Exists() && fr1.String() != "" && fr1.Type.String() != "Null" {
		t.Errorf("Expected finish_reason to be null in chunk1, got: %v", fr1.String())
	}

	// Chunk 2: Contains finishReason STOP + usage (final chunk, no functionCall)
	// This simulates what the upstream sends AFTER the tool call chunk
	chunk2 := []byte(`{"response":{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":20,"totalTokenCount":30}}}`)
	result2 := ConvertAntigravityResponseToOpenAI(ctx, "model", nil, nil, chunk2, &param)

	// Verify chunk2 has finish_reason: "tool_calls" (not "stop")
	if len(result2) != 1 {
		t.Fatalf("Expected 1 result from chunk2, got %d", len(result2))
	}
	fr2 := gjson.GetBytes(result2[0], "choices.0.finish_reason").String()
	if fr2 != "tool_calls" {
		t.Errorf("Expected finish_reason 'tool_calls', got: %s", fr2)
	}

	// Verify native_finish_reason is lowercase upstream value
	nfr2 := gjson.GetBytes(result2[0], "choices.0.native_finish_reason").String()
	if nfr2 != "stop" {
		t.Errorf("Expected native_finish_reason 'stop', got: %s", nfr2)
	}
}

func TestFinishReasonStopForNormalText(t *testing.T) {
	ctx := context.Background()
	var param any

	// Chunk 1: Text content only
	chunk1 := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"Hello world"}]}}]}}`)
	ConvertAntigravityResponseToOpenAI(ctx, "model", nil, nil, chunk1, &param)

	// Chunk 2: Final chunk with STOP
	chunk2 := []byte(`{"response":{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}}`)
	result2 := ConvertAntigravityResponseToOpenAI(ctx, "model", nil, nil, chunk2, &param)

	// Verify finish_reason is "stop" (no tool calls were made)
	fr := gjson.GetBytes(result2[0], "choices.0.finish_reason").String()
	if fr != "stop" {
		t.Errorf("Expected finish_reason 'stop', got: %s", fr)
	}
}

func TestFinishReasonMaxTokens(t *testing.T) {
	ctx := context.Background()
	var param any

	// Chunk 1: Text content
	chunk1 := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"Hello"}]}}]}}`)
	ConvertAntigravityResponseToOpenAI(ctx, "model", nil, nil, chunk1, &param)

	// Chunk 2: Final chunk with MAX_TOKENS
	chunk2 := []byte(`{"response":{"candidates":[{"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":100,"totalTokenCount":110}}}`)
	result2 := ConvertAntigravityResponseToOpenAI(ctx, "model", nil, nil, chunk2, &param)

	// Verify finish_reason is "max_tokens"
	fr := gjson.GetBytes(result2[0], "choices.0.finish_reason").String()
	if fr != "max_tokens" {
		t.Errorf("Expected finish_reason 'max_tokens', got: %s", fr)
	}
}

func TestToolCallTakesPriorityOverMaxTokens(t *testing.T) {
	ctx := context.Background()
	var param any

	// Chunk 1: Contains functionCall
	chunk1 := []byte(`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"test","args":{}}}]}}]}}`)
	ConvertAntigravityResponseToOpenAI(ctx, "model", nil, nil, chunk1, &param)

	// Chunk 2: Final chunk with MAX_TOKENS (but we had a tool call, so tool_calls should win)
	chunk2 := []byte(`{"response":{"candidates":[{"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":100,"totalTokenCount":110}}}`)
	result2 := ConvertAntigravityResponseToOpenAI(ctx, "model", nil, nil, chunk2, &param)

	// Verify finish_reason is "tool_calls" (takes priority over max_tokens)
	fr := gjson.GetBytes(result2[0], "choices.0.finish_reason").String()
	if fr != "tool_calls" {
		t.Errorf("Expected finish_reason 'tool_calls', got: %s", fr)
	}
}

func TestNoFinishReasonOnIntermediateChunks(t *testing.T) {
	ctx := context.Background()
	var param any

	// Chunk 1: Text content (no finish reason, no usage)
	chunk1 := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"Hello"}]}}]}}`)
	result1 := ConvertAntigravityResponseToOpenAI(ctx, "model", nil, nil, chunk1, &param)

	// Verify no finish_reason on intermediate chunk
	fr1 := gjson.GetBytes(result1[0], "choices.0.finish_reason")
	if fr1.Exists() && fr1.String() != "" && fr1.Type.String() != "Null" {
		t.Errorf("Expected no finish_reason on intermediate chunk, got: %v", fr1)
	}

	// Chunk 2: More text (no finish reason, no usage)
	chunk2 := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":" world"}]}}]}}`)
	result2 := ConvertAntigravityResponseToOpenAI(ctx, "model", nil, nil, chunk2, &param)

	// Verify no finish_reason on intermediate chunk
	fr2 := gjson.GetBytes(result2[0], "choices.0.finish_reason")
	if fr2.Exists() && fr2.String() != "" && fr2.Type.String() != "Null" {
		t.Errorf("Expected no finish_reason on intermediate chunk, got: %v", fr2)
	}
}

func TestConvertAntigravityResponseToOpenAICompletionTokensIncludeThoughts(t *testing.T) {
	tests := []struct {
		name       string
		chunk      string
		wantOutput int64
	}{
		{
			name:       "candidates and thoughts",
			chunk:      `{"response":{"usageMetadata":{"promptTokenCount":16,"candidatesTokenCount":5,"thoughtsTokenCount":42,"totalTokenCount":63}}}`,
			wantOutput: 47,
		},
		{
			name:       "thoughts without candidates",
			chunk:      `{"response":{"usageMetadata":{"promptTokenCount":16,"thoughtsTokenCount":42,"totalTokenCount":58}}}`,
			wantOutput: 42,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var param any
			result := ConvertAntigravityResponseToOpenAI(context.Background(), "model", nil, nil, []byte(tt.chunk), &param)
			if len(result) != 1 {
				t.Fatalf("expected 1 result, got %d", len(result))
			}
			completionTokens := gjson.GetBytes(result[0], "usage.completion_tokens")
			if !completionTokens.Exists() || completionTokens.Int() != tt.wantOutput {
				t.Fatalf("completion_tokens = %s, want present with value %d. Output: %s", completionTokens.Raw, tt.wantOutput, result[0])
			}
			if got := gjson.GetBytes(result[0], "usage.completion_tokens_details.reasoning_tokens").Int(); got != 42 {
				t.Fatalf("reasoning_tokens = %d, want 42. Output: %s", got, result[0])
			}
			promptTokens := gjson.GetBytes(result[0], "usage.prompt_tokens").Int()
			totalTokens := gjson.GetBytes(result[0], "usage.total_tokens").Int()
			if promptTokens+completionTokens.Int() != totalTokens {
				t.Fatalf("prompt_tokens + completion_tokens = %d, total_tokens = %d", promptTokens+completionTokens.Int(), totalTokens)
			}
		})
	}
}

func TestConvertAntigravityResponseToOpenAI_SynthesizesStopOnUsageChunkWhenFinishReasonOmitted(t *testing.T) {
	ctx := context.Background()
	var param any

	// Pure thinking chunk with usageMetadata but NO finishReason (reproducing real Antigravity upstream logs).
	// Usage alone does not make a chunk terminal: upstream can keep streaming after it.
	chunk := []byte(`{"response":{"candidates":[{"content":{"parts":[{"thought":true,"text":"Analyzing..."}]}}],"usageMetadata":{"promptTokenCount":100,"totalTokenCount":150,"thoughtsTokenCount":50},"modelVersion":"gemini-3.7-flash","responseId":"resp-999"}}`)
	result := ConvertAntigravityResponseToOpenAI(ctx, "gemini-3.7-flash", nil, nil, chunk, &param)
	if len(result) != 1 {
		t.Fatalf("expected 1 result from chunk, got %d", len(result))
	}
	if fr := gjson.GetBytes(result[0], "choices.0.finish_reason").String(); fr != "" {
		t.Fatalf("usage chunk without finishReason must not be terminal, got %q", fr)
	}

	// The terminal chunk is synthesized on [DONE] instead.
	done := ConvertAntigravityResponseToOpenAI(ctx, "gemini-3.7-flash", nil, nil, []byte("[DONE]"), &param)
	if len(done) != 1 {
		t.Fatalf("expected 1 synthesized terminal chunk on [DONE], got %d", len(done))
	}
	if fr := gjson.GetBytes(done[0], "choices.0.finish_reason").String(); fr != "stop" {
		t.Fatalf("terminal finish_reason = %q, want stop", fr)
	}
}

func TestConvertAntigravityResponseToOpenAI_PreservesFilteredUsageUntilDone(t *testing.T) {
	ctx := context.Background()
	var param any

	// Mirrors a payload already processed by FilterSSEUsageMetadata, which renames
	// non-terminal usage to cpaUsageMetadata. It is inlined so this package keeps no
	// dependency on the executor helpers; the executor tests cover the real filter.
	filtered := []byte(`{"response":{"candidates":[{"content":{"parts":[{"thought":true,"text":"Thinking..."}]}}],"modelVersion":"gemini-3.7-flash","responseId":"resp-filtered","cpaUsageMetadata":{"promptTokenCount":100,"totalTokenCount":150,"thoughtsTokenCount":50}}}`)
	result := ConvertAntigravityResponseToOpenAI(ctx, "gemini-3.7-flash", nil, nil, filtered, &param)
	if len(result) != 1 {
		t.Fatalf("expected 1 intermediate result, got %d", len(result))
	}
	if usage := gjson.GetBytes(result[0], "usage"); usage.Exists() {
		t.Fatalf("intermediate filtered usage should not be emitted: %s", usage.Raw)
	}
	if finishReason := gjson.GetBytes(result[0], "choices.0.finish_reason"); finishReason.String() != "" {
		t.Fatalf("intermediate finish_reason = %q, want null", finishReason.String())
	}

	done := ConvertAntigravityResponseToOpenAI(ctx, "gemini-3.7-flash", nil, nil, []byte("[DONE]"), &param)
	if len(done) != 1 {
		t.Fatalf("expected 1 synthetic terminal result, got %d", len(done))
	}
	if finishReason := gjson.GetBytes(done[0], "choices.0.finish_reason").String(); finishReason != "stop" {
		t.Fatalf("terminal finish_reason = %q, want stop", finishReason)
	}
	if got := gjson.GetBytes(done[0], "usage.prompt_tokens").Int(); got != 100 {
		t.Fatalf("terminal prompt_tokens = %d, want 100", got)
	}
	if got := gjson.GetBytes(done[0], "usage.total_tokens").Int(); got != 150 {
		t.Fatalf("terminal total_tokens = %d, want 150", got)
	}
}

func TestConvertAntigravityResponseToOpenAI_SynthesizesStopOnDoneWhenFinishReasonAndUsageOmitted(t *testing.T) {
	ctx := context.Background()
	var param any

	// Intermediate text chunk without finishReason or usage
	chunk := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"Partial output"}]}}],"modelVersion":"gemini-3.7-flash","responseId":"resp-888"}}`)
	result := ConvertAntigravityResponseToOpenAI(ctx, "gemini-3.7-flash", nil, nil, chunk, &param)
	if len(result) != 1 {
		t.Fatalf("expected 1 result from chunk, got %d", len(result))
	}
	fr1 := gjson.GetBytes(result[0], "choices.0.finish_reason")
	if fr1.Exists() && fr1.String() != "" && fr1.Type.String() != "Null" {
		t.Fatalf("expected finish_reason to be null on intermediate chunk, got %v", fr1)
	}

	// [DONE] without prior finishReason or usage -> synthesizes final chunk with finish_reason: "stop"
	done := ConvertAntigravityResponseToOpenAI(ctx, "gemini-3.7-flash", nil, nil, []byte("[DONE]"), &param)
	if len(done) != 1 {
		t.Fatalf("expected 1 result on [DONE], got %d", len(done))
	}
	frDone := gjson.GetBytes(done[0], "choices.0.finish_reason").String()
	if frDone != "stop" {
		t.Fatalf("expected finish_reason 'stop' on [DONE], got %q", frDone)
	}
	if mv := gjson.GetBytes(done[0], "model").String(); mv != "gemini-3.7-flash" {
		t.Fatalf("expected model 'gemini-3.7-flash', got %q", mv)
	}
	if rid := gjson.GetBytes(done[0], "id").String(); rid != "resp-888" {
		t.Fatalf("expected id 'resp-888', got %q", rid)
	}
}

func TestConvertAntigravityResponseToOpenAI_DoesNotFinalizeEmptyUpstreamStream(t *testing.T) {
	ctx := context.Background()
	var param any

	// A 200 response whose body carried no response chunk must not be reported as
	// a successful empty completion.
	done := ConvertAntigravityResponseToOpenAI(ctx, "gemini-3.7-flash", nil, nil, []byte("[DONE]"), &param)
	if len(done) != 0 {
		t.Fatalf("empty stream must not synthesize a terminal chunk, got %d results: %s", len(done), done[0])
	}
}

func TestConvertAntigravityResponseToOpenAI_ResponseFreeEventDoesNotStartStream(t *testing.T) {
	ctx := context.Background()
	var param any

	// A JSON keepalive or malformed envelope carries no candidates and no usage,
	// so it must not make an otherwise empty stream look complete.
	for _, chunk := range [][]byte{
		[]byte(`{}`),
		[]byte(`{"response":{}}`),
		[]byte(`{"response":{"candidates":[]}}`),
		[]byte(`{"response":{"candidates":[],"usageMetadata":{}}}`),
	} {
		ConvertAntigravityResponseToOpenAI(ctx, "gemini-3.7-flash", nil, nil, chunk, &param)
	}
	done := ConvertAntigravityResponseToOpenAI(ctx, "gemini-3.7-flash", nil, nil, []byte("[DONE]"), &param)
	if len(done) != 0 {
		t.Fatalf("response-free events must not synthesize a terminal chunk, got %d results: %s", len(done), done[0])
	}
}

func TestConvertAntigravityResponseToOpenAINonStreamRestoresDisambiguatedName(t *testing.T) {
	first := "mcp__plugin_cloudflare_cloudflare-builds__workers_builds_get_build"
	second := "mcp__plugin_cloudflare_cloudflare-builds__workers_builds_get_build_logs"
	original := []byte(`{"tools":[
		{"type":"function","function":{"name":"` + first + `"}},
		{"type":"function","function":{"name":"` + second + `"}}
	]}`)
	mapped := util.SanitizedFunctionNameMap(original)[second]
	responseJSON := []byte(`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"` + mapped + `","args":{}}}]}}]}}`)

	output := ConvertAntigravityResponseToOpenAINonStream(context.Background(), "gemini-3-flash", original, nil, responseJSON, nil)
	if got := gjson.GetBytes(output, "choices.0.message.tool_calls.0.function.name").String(); got != second {
		t.Fatalf("function.name = %q, want %q. Output: %s", got, second, output)
	}
}

func TestConvertAntigravityResponseToOpenAINonStreamIncludesReasoningContent(t *testing.T) {
	ctx := context.Background()
	responseJSON := []byte(`{
		"response": {
			"candidates": [{
				"index": 0,
				"content": {
					"parts": [
						{"text": "I need to multiply 17 by 24.", "thought": true},
						{"text": "408", "thoughtSignature": "sig-final-answer"}
					]
				},
				"finishReason": "STOP"
			}],
			"usageMetadata": {
				"promptTokenCount": 16,
				"candidatesTokenCount": 3,
				"thoughtsTokenCount": 42,
				"totalTokenCount": 61
			},
			"modelVersion": "gemini-3.1-pro-low",
			"responseId": "resp-reasoning"
		}
	}`)

	output := ConvertAntigravityResponseToOpenAINonStream(ctx, "gemini-3.1-pro-low", nil, nil, responseJSON, nil)
	if got := gjson.GetBytes(output, "choices.0.message.reasoning_content").String(); got != "I need to multiply 17 by 24." {
		t.Fatalf("reasoning_content = %q, want thought text. Output: %s", got, output)
	}
	if got := gjson.GetBytes(output, "choices.0.message.content").String(); got != "408" {
		t.Fatalf("content = %q, want final answer. Output: %s", got, output)
	}
	if got := gjson.GetBytes(output, "usage.completion_tokens_details.reasoning_tokens").Int(); got != 42 {
		t.Fatalf("reasoning_tokens = %d, want 42. Output: %s", got, output)
	}
}

func TestConvertAntigravityResponseToOpenAI_OmitToolCallsWhenAbsentInStreamingDelta(t *testing.T) {
	ctx := context.Background()
	var param any

	// 1. Text chunk: delta must omit tool_calls entirely instead of emitting "tool_calls": null.
	textChunk := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"Hello world"}]}}]}}`)
	textResults := ConvertAntigravityResponseToOpenAI(ctx, "gemini-3.5-flash-lite", nil, nil, textChunk, &param)
	if len(textResults) != 1 {
		t.Fatalf("expected 1 result, got %d", len(textResults))
	}
	if tc := gjson.GetBytes(textResults[0], "choices.0.delta.tool_calls"); tc.Exists() {
		t.Fatalf("choices.0.delta.tool_calls must not exist in text-only chunk, got: %s (type: %s)", tc.Raw, tc.Type)
	}

	// 2. Reasoning chunk: delta must omit tool_calls entirely.
	thoughtChunk := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"thinking...","thought":true}]}}]}}`)
	thoughtResults := ConvertAntigravityResponseToOpenAI(ctx, "gemini-3.5-flash-lite", nil, nil, thoughtChunk, &param)
	if len(thoughtResults) != 1 {
		t.Fatalf("expected 1 result, got %d", len(thoughtResults))
	}
	if tc := gjson.GetBytes(thoughtResults[0], "choices.0.delta.tool_calls"); tc.Exists() {
		t.Fatalf("choices.0.delta.tool_calls must not exist in reasoning chunk, got: %s (type: %s)", tc.Raw, tc.Type)
	}

	// 3. Tool call chunk: delta must emit tool_calls as an array.
	toolChunk := []byte(`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"get_weather","args":{"city":"Beijing"}}}]}}]}}`)
	toolResults := ConvertAntigravityResponseToOpenAI(ctx, "gemini-3.5-flash-lite", nil, nil, toolChunk, &param)
	if len(toolResults) != 1 {
		t.Fatalf("expected 1 result, got %d", len(toolResults))
	}
	tc := gjson.GetBytes(toolResults[0], "choices.0.delta.tool_calls")
	if !tc.Exists() || !tc.IsArray() || len(tc.Array()) != 1 {
		t.Fatalf("choices.0.delta.tool_calls must be an array with 1 call, got: %s", tc.Raw)
	}
	if gotName := tc.Array()[0].Get("function.name").String(); gotName != "get_weather" {
		t.Fatalf("function.name = %q, want get_weather", gotName)
	}
	if gotArgs := tc.Array()[0].Get("function.arguments").String(); gotArgs != `{"city":"Beijing"}` {
		t.Fatalf("function.arguments = %q, want {\"city\":\"Beijing\"}", gotArgs)
	}

	// 4. Final chunk with finishReason and usage: delta must omit tool_calls.
	finalChunk := []byte(`{"response":{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}}`)
	finalResults := ConvertAntigravityResponseToOpenAI(ctx, "gemini-3.5-flash-lite", nil, nil, finalChunk, &param)
	if len(finalResults) != 1 {
		t.Fatalf("expected 1 result, got %d", len(finalResults))
	}
	if tcFinal := gjson.GetBytes(finalResults[0], "choices.0.delta.tool_calls"); tcFinal.Exists() {
		t.Fatalf("choices.0.delta.tool_calls must not exist in final chunk, got: %s (type: %s)", tcFinal.Raw, tcFinal.Type)
	}

	// 5. Synthesized DONE terminal chunk: delta must omit tool_calls.
	var doneParam any
	ConvertAntigravityResponseToOpenAI(ctx, "gemini-3.5-flash-lite", nil, nil, textChunk, &doneParam)
	doneResults := ConvertAntigravityResponseToOpenAI(ctx, "gemini-3.5-flash-lite", nil, nil, []byte("[DONE]"), &doneParam)
	if len(doneResults) == 1 {
		if tcDone := gjson.GetBytes(doneResults[0], "choices.0.delta.tool_calls"); tcDone.Exists() {
			t.Fatalf("choices.0.delta.tool_calls must not exist in synthesized DONE chunk, got: %s (type: %s)", tcDone.Raw, tcDone.Type)
		}
	}
}
