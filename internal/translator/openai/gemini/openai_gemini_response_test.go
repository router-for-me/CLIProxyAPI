package gemini

import (
	"context"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestConvertOpenAIResponseToGeminiNonStreamPreservesToolCallID(t *testing.T) {
	raw := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_chat_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]}}]}`)
	out := ConvertOpenAIResponseToGeminiNonStream(context.Background(), "gpt-test", nil, nil, raw, nil)
	if got := ojson.GetBytesNoCopy(out, "candidates.0.content.parts.0.functionCall.id").String(); got != "call_chat_1" {
		t.Fatalf("functionCall.id = %q, want call_chat_1", got)
	}
	if got := ojson.GetBytesNoCopy(out, "candidates.0.content.parts.0.functionCall.args.q").String(); got != "x" {
		t.Fatalf("functionCall.args.q = %q, want x", got)
	}
}

func TestConvertOpenAIResponseToGeminiStreamPreservesToolCallID(t *testing.T) {
	var param any
	ConvertOpenAIResponseToGemini(context.Background(), "gpt-test", nil, nil, []byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_stream_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]}}]}`), &param)
	out := ConvertOpenAIResponseToGemini(context.Background(), "gpt-test", nil, nil, []byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`), &param)
	if len(out) == 0 {
		t.Fatalf("stream output is empty")
	}
	if got := ojson.GetBytesNoCopy(out[len(out)-1], "candidates.0.content.parts.0.functionCall.id").String(); got != "call_stream_1" {
		t.Fatalf("functionCall.id = %q, want call_stream_1", got)
	}
	if got := ojson.GetBytesNoCopy(out[len(out)-1], "candidates.0.content.parts.0.functionCall.args.q").String(); got != "x" {
		t.Fatalf("functionCall.args.q = %q, want x", got)
	}
}

func TestConvertOpenAIResponseToGeminiNonStream_MultiChoiceCandidatesIsolated(t *testing.T) {
	// Issue #8041 minimal reproduction: choice 0 has reasoning + visible text, choice 1 has visible text only.
	raw := []byte(`{
		"id": "chatcmpl-1",
		"object": "chat.completion",
		"model": "deepseek-r1",
		"choices": [
			{
				"index": 0,
				"message": {
					"role": "assistant",
					"reasoning_content": "secret thought 0",
					"content": "visible answer 0"
				},
				"finish_reason": "stop"
			},
			{
				"index": 1,
				"message": {
					"role": "assistant",
					"content": "visible answer 1"
				},
				"finish_reason": "stop"
			}
		],
		"usage": {
			"prompt_tokens": 10,
			"completion_tokens": 20,
			"total_tokens": 30
		}
	}`)
	out := ConvertOpenAIResponseToGeminiNonStream(context.Background(), "deepseek-r1", nil, nil, raw, nil)
	candidates := ojson.GetBytesNoCopy(out, "candidates").Array()
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d; output=%s", len(candidates), out)
	}

	// Candidate 0 preserves its thought part and visible text part.
	if got := candidates[0].Get("index").Int(); got != 0 {
		t.Fatalf("candidates[0].index = %d, want 0", got)
	}
	if got := candidates[0].Get("content.role").String(); got != "model" {
		t.Fatalf("candidates[0].content.role = %q, want model", got)
	}
	if got := candidates[0].Get("finishReason").String(); got != "STOP" {
		t.Fatalf("candidates[0].finishReason = %q, want STOP", got)
	}
	c0Parts := candidates[0].Get("content.parts").Array()
	if len(c0Parts) != 2 {
		t.Fatalf("candidates[0] expected 2 parts, got %d; output=%s", len(c0Parts), out)
	}
	if !c0Parts[0].Get("thought").Bool() || c0Parts[0].Get("text").String() != "secret thought 0" {
		t.Fatalf("unexpected candidates[0].parts[0]: %s", c0Parts[0].Raw)
	}
	if c0Parts[1].Get("thought").Exists() || c0Parts[1].Get("text").String() != "visible answer 0" {
		t.Fatalf("unexpected candidates[0].parts[1]: %s", c0Parts[1].Raw)
	}

	// Candidate 1 is isolated with visible text only and no leaked thought flag.
	if got := candidates[1].Get("index").Int(); got != 1 {
		t.Fatalf("candidates[1].index = %d, want 1", got)
	}
	if got := candidates[1].Get("content.role").String(); got != "model" {
		t.Fatalf("candidates[1].content.role = %q, want model", got)
	}
	if got := candidates[1].Get("finishReason").String(); got != "STOP" {
		t.Fatalf("candidates[1].finishReason = %q, want STOP", got)
	}
	c1Parts := candidates[1].Get("content.parts").Array()
	if len(c1Parts) != 1 {
		t.Fatalf("candidates[1] expected 1 part, got %d; output=%s", len(c1Parts), out)
	}
	if c1Parts[0].Get("thought").Exists() || c1Parts[0].Get("text").String() != "visible answer 1" {
		t.Fatalf("unexpected candidates[1].parts[0]: %s", c1Parts[0].Raw)
	}

	// Usage metadata and model are preserved at root.
	if got := ojson.GetBytesNoCopy(out, "model").String(); got != "deepseek-r1" {
		t.Fatalf("model = %q, want deepseek-r1", got)
	}
	if got := ojson.GetBytesNoCopy(out, "usageMetadata.totalTokenCount").Int(); got != 30 {
		t.Fatalf("usageMetadata.totalTokenCount = %d, want 30", got)
	}

	// Tool call in choice 0 and text in choice 1 must stay in separate candidates without merging into one Part.
	rawToolAndText := []byte(`{"choices":[
		{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"a\"}"}}]},"finish_reason":"tool_calls"},
		{"index":1,"message":{"role":"assistant","content":"choice 1 text"},"finish_reason":"length"}
	]}`)
	outToolAndText := ConvertOpenAIResponseToGeminiNonStream(context.Background(), "gpt-test", nil, nil, rawToolAndText, nil)
	ttCands := ojson.GetBytesNoCopy(outToolAndText, "candidates").Array()
	if len(ttCands) != 2 {
		t.Fatalf("expected 2 candidates for tool+text response, got %d; output=%s", len(ttCands), outToolAndText)
	}
	ttParts0 := ttCands[0].Get("content.parts").Array()
	if len(ttParts0) != 1 || ttParts0[0].Get("functionCall.id").String() != "call_1" || ttParts0[0].Get("text").Exists() {
		t.Fatalf("unexpected candidates[0] parts: %s", ttCands[0].Raw)
	}
	if got := ttCands[0].Get("finishReason").String(); got != "STOP" {
		t.Fatalf("candidates[0].finishReason = %q, want STOP", got)
	}
	ttParts1 := ttCands[1].Get("content.parts").Array()
	if len(ttParts1) != 1 || ttParts1[0].Get("text").String() != "choice 1 text" || ttParts1[0].Get("functionCall").Exists() {
		t.Fatalf("unexpected candidates[1] parts: %s", ttCands[1].Raw)
	}
	if got := ttCands[1].Get("finishReason").String(); got != "MAX_TOKENS" {
		t.Fatalf("candidates[1].finishReason = %q, want MAX_TOKENS", got)
	}

	// Null finish_reason on choice 1 must not inherit choice 0's finishReason, and reasoning/array content stays isolated per choice.
	rawMixed := []byte(`{"choices":[
		{"index":0,"message":{"role":"assistant","reasoning":"plan 0","content":[{"type":"text","text":"ans 0"}]},"finish_reason":"stop"},
		{"index":1,"message":{"role":"assistant","content":[{"type":"thinking","thinking":"plan 1"},{"type":"text","text":"ans 1"}]},"finish_reason":null}
	]}`)
	outMixed := ConvertOpenAIResponseToGeminiNonStream(context.Background(), "gemini-4-neon", nil, nil, rawMixed, nil)
	mixedCands := ojson.GetBytesNoCopy(outMixed, "candidates").Array()
	if len(mixedCands) != 2 {
		t.Fatalf("expected 2 candidates for mixed response, got %d; output=%s", len(mixedCands), outMixed)
	}
	if got := mixedCands[0].Get("finishReason").String(); got != "STOP" {
		t.Fatalf("mixedCands[0].finishReason = %q, want STOP", got)
	}
	if mixedCands[1].Get("finishReason").Exists() {
		t.Fatalf("mixedCands[1] unexpectedly inherited finishReason: %s", mixedCands[1].Raw)
	}
	m0Parts := mixedCands[0].Get("content.parts").Array()
	if len(m0Parts) != 2 || !m0Parts[0].Get("thought").Bool() || m0Parts[0].Get("text").String() != "plan 0" || m0Parts[1].Get("thought").Exists() || m0Parts[1].Get("text").String() != "ans 0" {
		t.Fatalf("unexpected mixedCands[0].parts: %s", mixedCands[0].Raw)
	}
	m1Parts := mixedCands[1].Get("content.parts").Array()
	if len(m1Parts) != 2 || !m1Parts[0].Get("thought").Bool() || m1Parts[0].Get("text").String() != "plan 1" || m1Parts[1].Get("thought").Exists() || m1Parts[1].Get("text").String() != "ans 1" {
		t.Fatalf("unexpected mixedCands[1].parts: %s", mixedCands[1].Raw)
	}

	// Non-assistant role from an OpenAI-compatible upstream must still preserve Gemini candidate role "model".
	rawRole := []byte(`{"choices":[{"index":0,"message":{"role":"tool","content":"ok"},"finish_reason":"stop"}]}`)
	outRole := ConvertOpenAIResponseToGeminiNonStream(context.Background(), "gpt-test", nil, nil, rawRole, nil)
	if got := ojson.GetBytesNoCopy(outRole, "candidates.0.content.role").String(); got != "model" {
		t.Fatalf("candidates.0.content.role = %q, want model", got)
	}
}

func TestConvertOpenAIResponseToGeminiStream_NullFinishReasonIgnored(t *testing.T) {
	var param any

	// Chunk 1: Contentless delta with explicit finish_reason: null (e.g. role declaration or pre-reasoning delta)
	chunk1 := []byte(`{"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`)
	out1 := ConvertOpenAIResponseToGemini(context.Background(), "gpt-test", nil, nil, chunk1, &param)
	for i, chunk := range out1 {
		if fr := ojson.GetBytesNoCopy(chunk, "candidates.0.finishReason"); fr.Exists() && fr.String() != "" {
			t.Fatalf("chunk1[%d] unexpectedly set finishReason = %q on non-final chunk; payload=%s", i, fr.String(), chunk)
		}
	}

	// Chunk 2: Reasoning delta with finish_reason: null
	chunk2 := []byte(`{"choices":[{"index":0,"delta":{"reasoning_content":"thinking..."},"finish_reason":null}]}`)
	out2 := ConvertOpenAIResponseToGemini(context.Background(), "gpt-test", nil, nil, chunk2, &param)
	if len(out2) == 0 {
		t.Fatalf("expected output for reasoning chunk, got 0 chunks")
	}
	if gotThought := ojson.GetBytesNoCopy(out2[0], "candidates.0.content.parts.0.thought").Bool(); !gotThought {
		t.Fatalf("expected thought: true on reasoning chunk, got false")
	}
	if gotText := ojson.GetBytesNoCopy(out2[0], "candidates.0.content.parts.0.text").String(); gotText != "thinking..." {
		t.Fatalf("expected reasoning text %q, got %q", "thinking...", gotText)
	}
	for i, chunk := range out2 {
		if fr := ojson.GetBytesNoCopy(chunk, "candidates.0.finishReason"); fr.Exists() && fr.String() != "" {
			t.Fatalf("chunk2[%d] unexpectedly set finishReason = %q on reasoning chunk; payload=%s", i, fr.String(), chunk)
		}
	}

	// Chunk 3: Contentless delta with finish_reason: "" (empty string should not be treated as stop)
	chunk3 := []byte(`{"choices":[{"index":0,"delta":{},"finish_reason":""}]}`)
	out3 := ConvertOpenAIResponseToGemini(context.Background(), "gpt-test", nil, nil, chunk3, &param)
	for i, chunk := range out3 {
		if fr := ojson.GetBytesNoCopy(chunk, "candidates.0.finishReason"); fr.Exists() && fr.String() != "" {
			t.Fatalf("chunk3[%d] unexpectedly set finishReason = %q on contentless chunk with empty finish_reason; payload=%s", i, fr.String(), chunk)
		}
	}

	// Chunk 4: Content delta with finish_reason: null
	chunk4 := []byte(`{"choices":[{"index":0,"delta":{"content":"hello world"},"finish_reason":null}]}`)
	out4 := ConvertOpenAIResponseToGemini(context.Background(), "gpt-test", nil, nil, chunk4, &param)
	if len(out4) == 0 {
		t.Fatalf("expected output for content chunk, got 0 chunks")
	}
	if gotText := ojson.GetBytesNoCopy(out4[0], "candidates.0.content.parts.0.text").String(); gotText != "hello world" {
		t.Fatalf("expected content text %q, got %q", "hello world", gotText)
	}
	for i, chunk := range out4 {
		if fr := ojson.GetBytesNoCopy(chunk, "candidates.0.finishReason"); fr.Exists() && fr.String() != "" {
			t.Fatalf("chunk4[%d] unexpectedly set finishReason = %q on non-final content chunk; payload=%s", i, fr.String(), chunk)
		}
	}

	// Chunk 5: Final chunk with finish_reason: "stop"
	chunk5 := []byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	out5 := ConvertOpenAIResponseToGemini(context.Background(), "gpt-test", nil, nil, chunk5, &param)
	if len(out5) == 0 {
		t.Fatalf("expected output for final chunk, got 0 chunks")
	}
	if got := ojson.GetBytesNoCopy(out5[len(out5)-1], "candidates.0.finishReason").String(); got != "STOP" {
		t.Fatalf("expected finishReason STOP on final chunk, got %q", got)
	}
}

func TestConvertOpenAIResponseToGeminiNonStream_NullFinishReasonIgnored(t *testing.T) {
	testCases := []struct {
		name    string
		payload []byte
	}{
		{
			name:    "null finish_reason",
			payload: []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":null}]}`),
		},
		{
			name:    "empty finish_reason",
			payload: []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":""}]}`),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			out := ConvertOpenAIResponseToGeminiNonStream(context.Background(), "gpt-test", nil, nil, tc.payload, nil)
			if fr := ojson.GetBytesNoCopy(out, "candidates.0.finishReason"); fr.Exists() && fr.String() != "" {
				t.Fatalf("unexpected finishReason = %q on non-stream response; output=%s", fr.String(), out)
			}
		})
	}
}

func TestConvertOpenAIResponseToGeminiNonStream_PreservesToolCallThoughtSignature(t *testing.T) {
	up := []byte(`{"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[
		{"id":"c1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"},"extra_content":{"google":{"thought_signature":"SIG"}}},
		{"id":"c2","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"London\"}"}}
	]}}]}`)
	res := ConvertOpenAIResponseToGeminiNonStream(context.Background(), "", nil, nil, up, nil)
	if got := ojson.GetBytesNoCopy(res, "candidates.0.content.parts.0.thoughtSignature").String(); got != "SIG" {
		t.Fatalf("candidates.0.content.parts.0.thoughtSignature = %q, want SIG; res=%s", got, string(res))
	}
	if ojson.GetBytesNoCopy(res, "candidates.0.content.parts.1.thoughtSignature").Exists() {
		t.Fatalf("expected unsigned second tool call not to have thoughtSignature; res=%s", string(res))
	}
}

func TestConvertOpenAIResponseToGeminiStream_PreservesToolCallThoughtSignature(t *testing.T) {
	var param any
	ConvertOpenAIResponseToGemini(context.Background(), "google/gemini-3-pro", nil, nil, []byte(`{"choices":[{"index":0,"delta":{"tool_calls":[
		{"index":1,"id":"c2","type":"function","function":{"name":"get_weather","arguments":"{\"city\":"}},
		{"index":0,"id":"c1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":"},"extra_content":{"google":{"thought_signature":"SIG"}}}
	]}}]}`), &param)
	ConvertOpenAIResponseToGemini(context.Background(), "google/gemini-3-pro", nil, nil, []byte(`{"choices":[{"index":0,"delta":{"tool_calls":[
		{"index":1,"function":{"arguments":"\"London\"}"}},
		{"index":0,"function":{"arguments":"\"Paris\"}"}}
	]}}]}`), &param)
	out := ConvertOpenAIResponseToGemini(context.Background(), "google/gemini-3-pro", nil, nil, []byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`), &param)
	if len(out) == 0 {
		t.Fatalf("stream output is empty")
	}
	last := out[len(out)-1]
	if got := ojson.GetBytesNoCopy(last, "candidates.0.content.parts.0.functionCall.id").String(); got != "c1" {
		t.Fatalf("candidates.0.content.parts.0.functionCall.id = %q, want c1; payload=%s", got, string(last))
	}
	if got := ojson.GetBytesNoCopy(last, "candidates.0.content.parts.0.thoughtSignature").String(); got != "SIG" {
		t.Fatalf("candidates.0.content.parts.0.thoughtSignature = %q, want SIG; payload=%s", got, string(last))
	}
	if got := ojson.GetBytesNoCopy(last, "candidates.0.content.parts.1.functionCall.id").String(); got != "c2" {
		t.Fatalf("candidates.0.content.parts.1.functionCall.id = %q, want c2; payload=%s", got, string(last))
	}
	if ojson.GetBytesNoCopy(last, "candidates.0.content.parts.1.thoughtSignature").Exists() {
		t.Fatalf("expected unsigned second tool call not to have thoughtSignature; payload=%s", string(last))
	}

	// Single-chunk tool call with finish_reason in the same SSE delta
	var singleChunkParam any
	singleOut := ConvertOpenAIResponseToGemini(context.Background(), "google/gemini-3-pro", nil, nil, []byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c_single","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Rome\"}"},"extra_content":{"google":{"thought_signature":"SIG_SINGLE"}}}]},"finish_reason":"tool_calls"}]}`), &singleChunkParam)
	if len(singleOut) == 0 {
		t.Fatalf("expected output for single-chunk tool_calls + finish_reason")
	}
	if got := ojson.GetBytesNoCopy(singleOut[len(singleOut)-1], "candidates.0.content.parts.0.thoughtSignature").String(); got != "SIG_SINGLE" {
		t.Fatalf("single-chunk thoughtSignature = %q, want SIG_SINGLE; payload=%s", got, string(singleOut[len(singleOut)-1]))
	}

	// Reasoning/content in the same delta as tool_calls, plus signature arriving in a later delta without function
	var mixedParam any
	mixedChunk1 := ConvertOpenAIResponseToGemini(context.Background(), "google/gemini-3-pro", nil, nil, []byte(`{"choices":[{"index":0,"delta":{"reasoning_content":"checking weather","tool_calls":[{"index":0,"id":"c_mixed","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Berlin\"}"}}]}}]}`), &mixedParam)
	if len(mixedChunk1) != 1 || !ojson.GetBytesNoCopy(mixedChunk1[0], "candidates.0.content.parts.0.thought").Bool() {
		t.Fatalf("expected reasoning chunk output on mixed delta, got %v", mixedChunk1)
	}
	ConvertOpenAIResponseToGemini(context.Background(), "google/gemini-3-pro", nil, nil, []byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"extra_content":{"google":{"thought_signature":"SIG_LATE"}}}]}}]}`), &mixedParam)
	mixedOut := ConvertOpenAIResponseToGemini(context.Background(), "google/gemini-3-pro", nil, nil, []byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`), &mixedParam)
	if len(mixedOut) == 0 {
		t.Fatalf("expected output when flushing mixed tool call")
	}
	if got := ojson.GetBytesNoCopy(mixedOut[len(mixedOut)-1], "candidates.0.content.parts.0.thoughtSignature").String(); got != "SIG_LATE" {
		t.Fatalf("mixed/late-signature thoughtSignature = %q, want SIG_LATE; payload=%s", got, string(mixedOut[len(mixedOut)-1]))
	}
}

func TestConvertOpenAIResponseToGeminiStream_ContentWithFinishReason(t *testing.T) {
	var param any
	ConvertOpenAIResponseToGemini(context.Background(), "glm-test", nil, nil, []byte(`{"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`), &param)
	out := ConvertOpenAIResponseToGemini(context.Background(), "glm-test", nil, nil, []byte(`{"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`), &param)
	var text, reason string
	for _, c := range out {
		if s := ojson.GetBytesNoCopy(c, "candidates.0.content.parts.0.text").String(); s != "" {
			text = s
		}
		if s := ojson.GetBytesNoCopy(c, "candidates.0.finishReason").String(); s != "" {
			reason = s
		}
	}
	if text != "ok" || reason != "STOP" {
		t.Fatalf("want text ok and finishReason STOP, got text=%q reason=%q chunks=%q", text, reason, out)
	}

	var reasoningParam any
	reasoningOut := ConvertOpenAIResponseToGemini(context.Background(), "glm-test", nil, nil, []byte(`{"choices":[{"index":0,"delta":{"reasoning_content":"thinking"},"finish_reason":"length"}]}`), &reasoningParam)
	var thoughtText, reasoningFinish string
	for _, c := range reasoningOut {
		if ojson.GetBytesNoCopy(c, "candidates.0.content.parts.0.thought").Bool() {
			thoughtText = ojson.GetBytesNoCopy(c, "candidates.0.content.parts.0.text").String()
		}
		if s := ojson.GetBytesNoCopy(c, "candidates.0.finishReason").String(); s != "" {
			reasoningFinish = s
		}
	}
	if thoughtText != "thinking" || reasoningFinish != "MAX_TOKENS" {
		t.Fatalf("want thoughtText thinking and finishReason MAX_TOKENS, got thought=%q reason=%q chunks=%q", thoughtText, reasoningFinish, reasoningOut)
	}
}

func TestConvertOpenAIResponseToGeminiNonStream_ReasoningAndReasoningDetails(t *testing.T) {
	// Reasoning field only with null content (issue #8034 minimal repro 1)
	rawReasoning := []byte(`{"id":"chatcmpl-1","model":"gemini-4-neon","choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning":"Thinking trace"},"finish_reason":"stop"}]}`)
	outReasoning := ConvertOpenAIResponseToGeminiNonStream(context.Background(), "gemini-4-neon", nil, nil, rawReasoning, nil)
	parts := ojson.GetBytesNoCopy(outReasoning, "candidates.0.content.parts").Array()
	if len(parts) != 1 {
		t.Fatalf("expected 1 part for reasoning-only response, got %d; output=%s", len(parts), outReasoning)
	}
	if !parts[0].Get("thought").Bool() || parts[0].Get("text").String() != "Thinking trace" {
		t.Fatalf("unexpected thought part: %s", parts[0].Raw)
	}
	if got := ojson.GetBytesNoCopy(outReasoning, "candidates.0.finishReason").String(); got != "STOP" {
		t.Fatalf("finishReason = %q, want STOP", got)
	}

	// reasoning_details array only (ignores encrypted entries without text)
	rawDetails := []byte(`{"id":"chatcmpl-2","model":"gemini-4-neon","choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_details":[{"type":"reasoning.text","text":"Step 1: analyze"},{"type":"reasoning.encrypted","data":"opaque"}]},"finish_reason":"stop"}]}`)
	outDetails := ConvertOpenAIResponseToGeminiNonStream(context.Background(), "gemini-4-neon", nil, nil, rawDetails, nil)
	detailsParts := ojson.GetBytesNoCopy(outDetails, "candidates.0.content.parts").Array()
	if len(detailsParts) != 1 || !detailsParts[0].Get("thought").Bool() || detailsParts[0].Get("text").String() != "Step 1: analyze" {
		t.Fatalf("unexpected reasoning_details parts: %s", outDetails)
	}

	// Priority and deduplication when multiple reasoning fields are present
	rawDup := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"Done","reasoning":"Deduped thought","reasoning_details":[{"type":"reasoning.text","text":"Deduped thought"}]},"finish_reason":"stop"}]}`)
	outDup := ConvertOpenAIResponseToGeminiNonStream(context.Background(), "gemini-4-neon", nil, nil, rawDup, nil)
	dupParts := ojson.GetBytesNoCopy(outDup, "candidates.0.content.parts").Array()
	if len(dupParts) != 2 {
		t.Fatalf("expected 2 parts (1 thought + 1 text), got %d; output=%s", len(dupParts), outDup)
	}
	if !dupParts[0].Get("thought").Bool() || dupParts[0].Get("text").String() != "Deduped thought" {
		t.Fatalf("unexpected first part: %s", dupParts[0].Raw)
	}
	if dupParts[1].Get("thought").Bool() || dupParts[1].Get("text").String() != "Done" {
		t.Fatalf("unexpected second part: %s", dupParts[1].Raw)
	}

	// Empty/null reasoning_content falls back to reasoning
	rawFallback := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"","reasoning":"Fallback thought","content":"Answer"},"finish_reason":"stop"}]}`)
	outFallback := ConvertOpenAIResponseToGeminiNonStream(context.Background(), "gemini-4-neon", nil, nil, rawFallback, nil)
	fallbackParts := ojson.GetBytesNoCopy(outFallback, "candidates.0.content.parts").Array()
	if len(fallbackParts) != 2 || !fallbackParts[0].Get("thought").Bool() || fallbackParts[0].Get("text").String() != "Fallback thought" {
		t.Fatalf("expected fallback thought part, got output=%s", outFallback)
	}

	// reasoning_details with reasoning.summary only (ignores encrypted entries)
	rawSummary := []byte(`{"id":"chatcmpl-summary","model":"gemini-4-neon","choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_details":[{"type":"reasoning.summary","summary":"Summary trace"},{"type":"reasoning.encrypted","data":"opaque"}]},"finish_reason":"stop"}]}`)
	outSummary := ConvertOpenAIResponseToGeminiNonStream(context.Background(), "gemini-4-neon", nil, nil, rawSummary, nil)
	summaryParts := ojson.GetBytesNoCopy(outSummary, "candidates.0.content.parts").Array()
	if len(summaryParts) != 1 || !summaryParts[0].Get("thought").Bool() || summaryParts[0].Get("text").String() != "Summary trace" {
		t.Fatalf("unexpected reasoning.summary parts: %s", outSummary)
	}
}

func TestConvertOpenAIResponseToGeminiStream_ReasoningAndReasoningDetails(t *testing.T) {
	var param any
	// Delta with both reasoning and reasoning_details must not duplicate thought chunks
	out1 := ConvertOpenAIResponseToGemini(context.Background(), "gemini-4-neon", nil, nil, []byte(`{"choices":[{"index":0,"delta":{"reasoning":"Step 1","reasoning_details":[{"type":"reasoning.text","text":"Step 1"}]},"finish_reason":null}]}`), &param)
	if len(out1) != 1 {
		t.Fatalf("expected 1 thought chunk, got %d; chunks=%q", len(out1), out1)
	}
	if !ojson.GetBytesNoCopy(out1[0], "candidates.0.content.parts.0.thought").Bool() || ojson.GetBytesNoCopy(out1[0], "candidates.0.content.parts.0.text").String() != "Step 1" {
		t.Fatalf("unexpected chunk1: %s", out1[0])
	}

	// Delta with reasoning_details only
	out2 := ConvertOpenAIResponseToGemini(context.Background(), "gemini-4-neon", nil, nil, []byte(`{"choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.text","text":"Step 2"},{"type":"reasoning.encrypted","data":"opaque"}]},"finish_reason":null}]}`), &param)
	if len(out2) != 1 || !ojson.GetBytesNoCopy(out2[0], "candidates.0.content.parts.0.thought").Bool() || ojson.GetBytesNoCopy(out2[0], "candidates.0.content.parts.0.text").String() != "Step 2" {
		t.Fatalf("unexpected chunk2: %q", out2)
	}

	// Delta with empty reasoning_content falls back to reasoning alongside finish_reason
	out3 := ConvertOpenAIResponseToGemini(context.Background(), "gemini-4-neon", nil, nil, []byte(`{"choices":[{"index":0,"delta":{"reasoning_content":"","reasoning":"Step 3"},"finish_reason":"stop"}]}`), &param)
	var gotThought, gotFinish string
	for _, c := range out3 {
		if ojson.GetBytesNoCopy(c, "candidates.0.content.parts.0.thought").Bool() {
			gotThought = ojson.GetBytesNoCopy(c, "candidates.0.content.parts.0.text").String()
		}
		if fr := ojson.GetBytesNoCopy(c, "candidates.0.finishReason").String(); fr != "" {
			gotFinish = fr
		}
	}
	if gotThought != "Step 3" || gotFinish != "STOP" {
		t.Fatalf("want thought 'Step 3' and finishReason STOP, got thought=%q finish=%q chunks=%q", gotThought, gotFinish, out3)
	}

	// Delta with reasoning.summary only (ignores encrypted entries)
	var summaryParam any
	outSummary := ConvertOpenAIResponseToGemini(context.Background(), "gemini-4-neon", nil, nil, []byte(`{"choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.summary","summary":"Stream summary"},{"type":"reasoning.encrypted","data":"opaque"}]},"finish_reason":null}]}`), &summaryParam)
	if len(outSummary) != 1 || !ojson.GetBytesNoCopy(outSummary[0], "candidates.0.content.parts.0.thought").Bool() || ojson.GetBytesNoCopy(outSummary[0], "candidates.0.content.parts.0.text").String() != "Stream summary" {
		t.Fatalf("unexpected reasoning.summary stream output: %q", outSummary)
	}
}

func TestConvertOpenAIResponseToGeminiNonStream_ArrayContent(t *testing.T) {
	// Issue #8034 minimal repro 2
	raw := []byte(`{"id":"chatcmpl-2","model":"gemini-4-neon","choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"thinking","thinking":"plan"},{"type":"text","text":"Hello"}]},"finish_reason":"stop"}]}`)
	out := ConvertOpenAIResponseToGeminiNonStream(context.Background(), "gemini-4-neon", nil, nil, raw, nil)
	parts := ojson.GetBytesNoCopy(out, "candidates.0.content.parts").Array()
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d; output=%s", len(parts), out)
	}
	if !parts[0].Get("thought").Bool() || parts[0].Get("text").String() != "plan" {
		t.Fatalf("unexpected part[0]: %s", parts[0].Raw)
	}
	if parts[1].Get("thought").Bool() || parts[1].Get("text").String() != "Hello" {
		t.Fatalf("unexpected part[1]: %s", parts[1].Raw)
	}

	// Extended reasoning/text/refusal types and string item
	rawExt := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"reasoning","reasoning":"r1"},{"type":"thought","text":"r2"},{"type":"reasoning.text","text":"r3"},{"type":"reasoning_text","text":"r4"},{"type":"output_text","text":"out"},{"type":"refusal","refusal":"cannot do"},{"type":"refusal","text":"fallback refusal"},"raw string",{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]},"finish_reason":"stop"}]}`)
	outExt := ConvertOpenAIResponseToGeminiNonStream(context.Background(), "gemini-4-neon", nil, nil, rawExt, nil)
	extParts := ojson.GetBytesNoCopy(outExt, "candidates.0.content.parts").Array()
	want := []struct {
		thought bool
		text    string
	}{
		{true, "r1"},
		{true, "r2"},
		{true, "r3"},
		{true, "r4"},
		{false, "out"},
		{false, "cannot do"},
		{false, "fallback refusal"},
		{false, "raw string"},
	}
	if len(extParts) != len(want) {
		t.Fatalf("expected %d parts, got %d; output=%s", len(want), len(extParts), outExt)
	}
	for i, w := range want {
		if extParts[i].Get("thought").Bool() != w.thought || extParts[i].Get("text").String() != w.text {
			t.Fatalf("part[%d] = (thought=%v, text=%q), want (thought=%v, text=%q)", i, extParts[i].Get("thought").Bool(), extParts[i].Get("text").String(), w.thought, w.text)
		}
	}

	// Empty content array must not emit literal "[]"
	rawEmpty := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":[]},"finish_reason":"stop"}]}`)
	outEmpty := ConvertOpenAIResponseToGeminiNonStream(context.Background(), "gemini-4-neon", nil, nil, rawEmpty, nil)
	if got := len(ojson.GetBytesNoCopy(outEmpty, "candidates.0.content.parts").Array()); got != 0 {
		t.Fatalf("expected 0 parts for empty content array, got %d; output=%s", got, outEmpty)
	}
}

func TestConvertOpenAIResponseToGeminiStream_ArrayContent(t *testing.T) {
	var param any
	chunk := []byte(`{"choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":"plan"},{"type":"text","text":"Hello"},{"type":"output_text","text":" world"},{"type":"refusal","refusal":"nope"}]},"finish_reason":"stop"}]}`)
	out := ConvertOpenAIResponseToGemini(context.Background(), "gemini-4-neon", nil, nil, chunk, &param)

	type seg struct {
		thought bool
		text    string
	}
	var gotSegs []seg
	var gotFinish string
	for _, c := range out {
		if fr := ojson.GetBytesNoCopy(c, "candidates.0.finishReason").String(); fr != "" {
			gotFinish = fr
		}
		part := ojson.GetBytesNoCopy(c, "candidates.0.content.parts.0")
		if part.Exists() {
			gotSegs = append(gotSegs, seg{
				thought: part.Get("thought").Bool(),
				text:    part.Get("text").String(),
			})
		}
	}
	wantSegs := []seg{
		{true, "plan"},
		{false, "Hello"},
		{false, " world"},
		{false, "nope"},
	}
	if len(gotSegs) != len(wantSegs) {
		t.Fatalf("got %d segments %+v, want %+v; chunks=%q", len(gotSegs), gotSegs, wantSegs, out)
	}
	for i := range wantSegs {
		if gotSegs[i] != wantSegs[i] {
			t.Fatalf("seg[%d] = %+v, want %+v", i, gotSegs[i], wantSegs[i])
		}
	}
	if gotFinish != "STOP" {
		t.Fatalf("finishReason = %q, want STOP", gotFinish)
	}

	// Empty array delta must not emit literal "[]"
	var emptyParam any
	outEmpty := ConvertOpenAIResponseToGemini(context.Background(), "gemini-4-neon", nil, nil, []byte(`{"choices":[{"index":0,"delta":{"content":[]},"finish_reason":null}]}`), &emptyParam)
	if len(outEmpty) != 0 {
		t.Fatalf("expected 0 chunks for empty array delta, got %d: %q", len(outEmpty), outEmpty)
	}
}
