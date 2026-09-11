package gemini

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponseToGeminiNonStreamPreservesToolCallID(t *testing.T) {
	raw := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_chat_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]}}]}`)
	out := ConvertOpenAIResponseToGeminiNonStream(context.Background(), "gpt-test", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "candidates.0.content.parts.0.functionCall.id").String(); got != "call_chat_1" {
		t.Fatalf("functionCall.id = %q, want call_chat_1", got)
	}
	if got := gjson.GetBytes(out, "candidates.0.content.parts.0.functionCall.args.q").String(); got != "x" {
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
	if got := gjson.GetBytes(out[len(out)-1], "candidates.0.content.parts.0.functionCall.id").String(); got != "call_stream_1" {
		t.Fatalf("functionCall.id = %q, want call_stream_1", got)
	}
	if got := gjson.GetBytes(out[len(out)-1], "candidates.0.content.parts.0.functionCall.args.q").String(); got != "x" {
		t.Fatalf("functionCall.args.q = %q, want x", got)
	}
}

func TestConvertOpenAIResponseToGeminiNonStream_MultiChoicePartsOverlay(t *testing.T) {
	// Scenario 1: First choice has tool call, second choice has text on part 0 -> fields merge
	raw1 := []byte(`{"choices":[
		{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}},
		{"index":1,"message":{"role":"assistant","content":"choice 1 text"}}
	]}`)
	out1 := ConvertOpenAIResponseToGeminiNonStream(context.Background(), "gpt-test", nil, nil, raw1, nil)
	parts1 := gjson.GetBytes(out1, "candidates.0.content.parts").Array()
	if len(parts1) != 1 {
		t.Fatalf("expected 1 merged part, got %d. Output: %s", len(parts1), out1)
	}
	if parts1[0].Get("text").String() != "choice 1 text" {
		t.Fatalf("expected text to be 'choice 1 text', got %q", parts1[0].Get("text").String())
	}
	if parts1[0].Get("functionCall.id").String() != "call_1" {
		t.Fatalf("expected functionCall.id to be preserved as 'call_1', got %q", parts1[0].Get("functionCall.id").String())
	}

	// Scenario 2: Reasoning in choice 0, text in choice 1 on part 0 -> thought preserved, text updated
	raw2 := []byte(`{"choices":[
		{"index":0,"message":{"role":"assistant","reasoning_content":"initial thought"}},
		{"index":1,"message":{"role":"assistant","content":"final text"}}
	]}`)
	out2 := ConvertOpenAIResponseToGeminiNonStream(context.Background(), "gpt-test", nil, nil, raw2, nil)
	parts2 := gjson.GetBytes(out2, "candidates.0.content.parts").Array()
	if len(parts2) != 1 {
		t.Fatalf("expected 1 merged part, got %d. Output: %s", len(parts2), out2)
	}
	if !parts2[0].Get("thought").Bool() {
		t.Fatalf("expected thought: true to be preserved")
	}
	if parts2[0].Get("text").String() != "final text" {
		t.Fatalf("expected text to be 'final text', got %q", parts2[0].Get("text").String())
	}

	// Scenario 3: Text in choice 0, functionCall in choice 1 on part 0 -> text preserved, functionCall added
	raw3 := []byte(`{"choices":[
		{"index":0,"message":{"role":"assistant","content":"original text"}},
		{"index":1,"message":{"role":"assistant","tool_calls":[{"id":"call_2","type":"function","function":{"name":"search","arguments":"{}"}}]}}
	]}`)
	out3 := ConvertOpenAIResponseToGeminiNonStream(context.Background(), "gpt-test", nil, nil, raw3, nil)
	parts3 := gjson.GetBytes(out3, "candidates.0.content.parts").Array()
	if len(parts3) != 1 {
		t.Fatalf("expected 1 merged part, got %d. Output: %s", len(parts3), out3)
	}
	if parts3[0].Get("text").String() != "original text" {
		t.Fatalf("expected text to be 'original text', got %q", parts3[0].Get("text").String())
	}
	if parts3[0].Get("functionCall.id").String() != "call_2" {
		t.Fatalf("expected functionCall.id to be 'call_2', got %q", parts3[0].Get("functionCall.id").String())
	}
}

func TestConvertOpenAIResponseToGeminiStream_NullFinishReasonIgnored(t *testing.T) {
	var param any

	// Chunk 1: Contentless delta with explicit finish_reason: null (e.g. role declaration or pre-reasoning delta)
	chunk1 := []byte(`{"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`)
	out1 := ConvertOpenAIResponseToGemini(context.Background(), "gpt-test", nil, nil, chunk1, &param)
	for i, chunk := range out1 {
		if fr := gjson.GetBytes(chunk, "candidates.0.finishReason"); fr.Exists() && fr.String() != "" {
			t.Fatalf("chunk1[%d] unexpectedly set finishReason = %q on non-final chunk; payload=%s", i, fr.String(), chunk)
		}
	}

	// Chunk 2: Reasoning delta with finish_reason: null
	chunk2 := []byte(`{"choices":[{"index":0,"delta":{"reasoning_content":"thinking..."},"finish_reason":null}]}`)
	out2 := ConvertOpenAIResponseToGemini(context.Background(), "gpt-test", nil, nil, chunk2, &param)
	if len(out2) == 0 {
		t.Fatalf("expected output for reasoning chunk, got 0 chunks")
	}
	if gotThought := gjson.GetBytes(out2[0], "candidates.0.content.parts.0.thought").Bool(); !gotThought {
		t.Fatalf("expected thought: true on reasoning chunk, got false")
	}
	if gotText := gjson.GetBytes(out2[0], "candidates.0.content.parts.0.text").String(); gotText != "thinking..." {
		t.Fatalf("expected reasoning text %q, got %q", "thinking...", gotText)
	}
	for i, chunk := range out2 {
		if fr := gjson.GetBytes(chunk, "candidates.0.finishReason"); fr.Exists() && fr.String() != "" {
			t.Fatalf("chunk2[%d] unexpectedly set finishReason = %q on reasoning chunk; payload=%s", i, fr.String(), chunk)
		}
	}

	// Chunk 3: Contentless delta with finish_reason: "" (empty string should not be treated as stop)
	chunk3 := []byte(`{"choices":[{"index":0,"delta":{},"finish_reason":""}]}`)
	out3 := ConvertOpenAIResponseToGemini(context.Background(), "gpt-test", nil, nil, chunk3, &param)
	for i, chunk := range out3 {
		if fr := gjson.GetBytes(chunk, "candidates.0.finishReason"); fr.Exists() && fr.String() != "" {
			t.Fatalf("chunk3[%d] unexpectedly set finishReason = %q on contentless chunk with empty finish_reason; payload=%s", i, fr.String(), chunk)
		}
	}

	// Chunk 4: Content delta with finish_reason: null
	chunk4 := []byte(`{"choices":[{"index":0,"delta":{"content":"hello world"},"finish_reason":null}]}`)
	out4 := ConvertOpenAIResponseToGemini(context.Background(), "gpt-test", nil, nil, chunk4, &param)
	if len(out4) == 0 {
		t.Fatalf("expected output for content chunk, got 0 chunks")
	}
	if gotText := gjson.GetBytes(out4[0], "candidates.0.content.parts.0.text").String(); gotText != "hello world" {
		t.Fatalf("expected content text %q, got %q", "hello world", gotText)
	}
	for i, chunk := range out4 {
		if fr := gjson.GetBytes(chunk, "candidates.0.finishReason"); fr.Exists() && fr.String() != "" {
			t.Fatalf("chunk4[%d] unexpectedly set finishReason = %q on non-final content chunk; payload=%s", i, fr.String(), chunk)
		}
	}

	// Chunk 5: Final chunk with finish_reason: "stop"
	chunk5 := []byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	out5 := ConvertOpenAIResponseToGemini(context.Background(), "gpt-test", nil, nil, chunk5, &param)
	if len(out5) == 0 {
		t.Fatalf("expected output for final chunk, got 0 chunks")
	}
	if got := gjson.GetBytes(out5[len(out5)-1], "candidates.0.finishReason").String(); got != "STOP" {
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
			if fr := gjson.GetBytes(out, "candidates.0.finishReason"); fr.Exists() && fr.String() != "" {
				t.Fatalf("unexpected finishReason = %q on non-stream response; output=%s", fr.String(), out)
			}
		})
	}
}
