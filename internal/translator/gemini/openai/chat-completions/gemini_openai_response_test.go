package chat_completions

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiResponseToOpenAICompletionTokensIncludeThoughts(t *testing.T) {
	var param any
	chunk := []byte(`{"usageMetadata":{"promptTokenCount":16,"candidatesTokenCount":5,"thoughtsTokenCount":42,"totalTokenCount":63}}`)

	result := ConvertGeminiResponseToOpenAI(context.Background(), "model", nil, nil, chunk, &param)
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
	completionTokens := gjson.GetBytes(result[0], "usage.completion_tokens")
	if !completionTokens.Exists() || completionTokens.Int() != 47 {
		t.Fatalf("completion_tokens = %s, want present with value 47. Output: %s", completionTokens.Raw, result[0])
	}
}

func TestConvertGeminiResponseToOpenAINonStreamCompletionTokensIncludeThoughts(t *testing.T) {
	response := []byte(`{"usageMetadata":{"promptTokenCount":16,"thoughtsTokenCount":42,"totalTokenCount":58}}`)

	result := ConvertGeminiResponseToOpenAINonStream(context.Background(), "model", nil, nil, response, nil)
	completionTokens := gjson.GetBytes(result, "usage.completion_tokens")
	if !completionTokens.Exists() || completionTokens.Int() != 42 {
		t.Fatalf("completion_tokens = %s, want present with value 42. Output: %s", completionTokens.Raw, result)
	}
}

func TestGeminiFinishReasonOnlyOnFinalChunk(t *testing.T) {
	ctx := context.Background()
	var param any

	chunk1 := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"list_dir","args":{"path":"C:/"}}}]}}],"usageMetadata":{"trafficType":"ON_DEMAND"}}`)
	result1 := ConvertGeminiResponseToOpenAI(ctx, "model", nil, nil, chunk1, &param)
	if len(result1) != 1 {
		t.Fatalf("expected 1 result from chunk1, got %d", len(result1))
	}
	fr1 := gjson.GetBytes(result1[0], "choices.0.finish_reason")
	if fr1.Exists() && fr1.String() != "" && fr1.Type.String() != "Null" {
		t.Fatalf("expected null finish_reason on tool chunk, got %v", fr1.String())
	}

	chunk2 := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"list_dir","args":{"path":"D:/"}}}]}}],"usageMetadata":{"trafficType":"ON_DEMAND"}}`)
	ConvertGeminiResponseToOpenAI(ctx, "model", nil, nil, chunk2, &param)

	chunk3 := []byte(`{"candidates":[{"content":{"parts":[{"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`)
	result3 := ConvertGeminiResponseToOpenAI(ctx, "model", nil, nil, chunk3, &param)
	if len(result3) != 1 {
		t.Fatalf("expected 1 result from chunk3, got %d", len(result3))
	}
	fr3 := gjson.GetBytes(result3[0], "choices.0.finish_reason").String()
	if fr3 != "tool_calls" {
		t.Fatalf("expected finish_reason tool_calls, got %s", fr3)
	}
	nfr3 := gjson.GetBytes(result3[0], "choices.0.native_finish_reason").String()
	if nfr3 != "stop" {
		t.Fatalf("expected native_finish_reason stop, got %s", nfr3)
	}
}

func TestConvertGeminiResponseToOpenAINonStream_EmptyTextProducesEmptyString(t *testing.T) {
	response := []byte(`{"candidates":[{"content":{"parts":[{"text":""},{"text":"","thought":true}]},"finishReason":"STOP"}]}`)
	result := ConvertGeminiResponseToOpenAINonStream(context.Background(), "model", nil, nil, response, nil)

	content := gjson.GetBytes(result, "choices.0.message.content")
	if !content.Exists() || content.String() != "" || content.Type == gjson.Null {
		t.Fatalf("expected content to be empty string \"\", got %v (type %v)", content.Value(), content.Type)
	}

	reasoning := gjson.GetBytes(result, "choices.0.message.reasoning_content")
	if !reasoning.Exists() || reasoning.String() != "" || reasoning.Type == gjson.Null {
		t.Fatalf("expected reasoning_content to be empty string \"\", got %v (type %v)", reasoning.Value(), reasoning.Type)
	}
}

// Gemini 3.5 Transcribe returns its transcript in an audioTranscription part instead
// of the regular text part. Without handling it, the transcript is silently dropped
// and the OpenAI client sees an empty assistant message.
func TestConvertGeminiResponseToOpenAINonStream_AudioTranscriptionPartProducesContent(t *testing.T) {
	response := []byte(`{"candidates":[{"content":{"parts":[{"text":""},{"audioTranscription":{"text":"Hello world"}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":185,"totalTokenCount":185}}`)
	result := ConvertGeminiResponseToOpenAINonStream(context.Background(), "model", nil, nil, response, nil)

	content := gjson.GetBytes(result, "choices.0.message.content")
	if !content.Exists() || content.String() != "Hello world" {
		t.Fatalf("expected content to carry the transcript, got %v. Output: %s", content.Raw, result)
	}
}

func TestConvertGeminiResponseToOpenAINonStream_SingleAudioTranscriptionPart(t *testing.T) {
	response := []byte(`{"candidates":[{"content":{"parts":[{"audioTranscription":{"text":"Single transcription part"}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":185,"totalTokenCount":185}}`)
	result := ConvertGeminiResponseToOpenAINonStream(context.Background(), "model", nil, nil, response, nil)

	content := gjson.GetBytes(result, "choices.0.message.content")
	if !content.Exists() || content.String() != "Single transcription part" {
		t.Fatalf("expected content to carry the transcript, got %v. Output: %s", content.Raw, result)
	}
}

func TestConvertGeminiResponseToOpenAI_AudioTranscriptionPartStreamsContent(t *testing.T) {
	ctx := context.Background()
	var param any

	chunk := []byte(`{"candidates":[{"content":{"parts":[{"text":""},{"audioTranscription":{"text":"Testing one two three."}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":185,"candidatesTokenCount":8,"totalTokenCount":193}}`)
	result := ConvertGeminiResponseToOpenAI(ctx, "model", nil, nil, chunk, &param)
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}

	content := gjson.GetBytes(result[0], "choices.0.delta.content")
	if !content.Exists() || content.String() != "Testing one two three." {
		t.Fatalf("expected delta.content to carry the transcript, got %v. Output: %s", content.Raw, result[0])
	}
}

func TestConvertGeminiResponseToOpenAINonStream_TextPrecedenceOverAudioTranscription(t *testing.T) {
	response := []byte(`{"candidates":[{"content":{"parts":[{"text":"explicit text","audioTranscription":{"text":"ignored transcription"}}]},"finishReason":"STOP"}]}`)
	result := ConvertGeminiResponseToOpenAINonStream(context.Background(), "model", nil, nil, response, nil)

	content := gjson.GetBytes(result, "choices.0.message.content")
	if !content.Exists() || content.String() != "explicit text" {
		t.Fatalf("expected content to prioritize explicit text, got %v. Output: %s", content.Raw, result)
	}
}

func TestConvertGeminiResponseToOpenAI_PinsSanitizedNameMapPerStream(t *testing.T) {
	ctx := context.Background()

	// 1. Only the first chunk carries the request with a sanitized tool name.
	var param any
	reqWithTool := []byte(`{"model":"gemini-2.5-flash","tools":[{"name":"mcp/search"}]}`)
	chunk1 := []byte(`{"candidates":[{"content":{"parts":[{"text":"thinking"}]}}]}`)
	chunk2 := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"mcp_search","args":{"q":"test"}}}]}}]}`)

	ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-flash", reqWithTool, nil, chunk1, &param)
	out := ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-flash", nil, nil, chunk2, &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk output, got %d", len(out))
	}
	if got := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.function.name").String(); got != "mcp/search" {
		t.Fatalf("restored tool name = %q, want mcp/search", got)
	}

	// 2. When the first chunk's request produces a nil SanitizedNameMap, later chunks must not rebuild it.
	var nilMapParam any
	reqNoSanitize := []byte(`{"model":"gemini-2.5-flash","tools":[{"name":"plain_tool"}]}`)
	ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-flash", reqNoSanitize, nil, chunk1, &nilMapParam)
	outNil := ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-flash", reqWithTool, nil, chunk2, &nilMapParam)
	if len(outNil) != 1 {
		t.Fatalf("expected 1 chunk output, got %d", len(outNil))
	}
	if got := gjson.GetBytes(outNil[0], "choices.0.delta.tool_calls.0.function.name").String(); got != "mcp_search" {
		t.Fatalf("tool name = %q, want mcp_search (must not rebuild nil SanitizedNameMap on later chunks)", got)
	}
}

func BenchmarkConvertGeminiResponseToOpenAI_TextDelta(b *testing.B) {
	request := []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"` + strings.Repeat("x", 1024*1024) + `"}]}`)
	delta := []byte(`{"candidates":[{"content":{"parts":[{"text":"token "}]}}],"modelVersion":"gemini-2.5-flash","responseId":"resp_bench"}`)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var param any
		for j := 0; j < 64; j++ {
			ConvertGeminiResponseToOpenAI(context.Background(), "gemini-2.5-flash", request, nil, delta, &param)
		}
	}
}
