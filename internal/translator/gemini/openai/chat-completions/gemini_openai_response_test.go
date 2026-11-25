package chat_completions

import (
	"context"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestConvertGeminiResponseToOpenAICompletionTokensIncludeThoughts(t *testing.T) {
	var param any
	chunk := []byte(`{"usageMetadata":{"promptTokenCount":16,"candidatesTokenCount":5,"thoughtsTokenCount":42,"totalTokenCount":63}}`)

	result := ConvertGeminiResponseToOpenAI(context.Background(), "model", nil, nil, chunk, &param)
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
	completionTokens := ojson.GetBytesNoCopy(result[0], "usage.completion_tokens")
	if !completionTokens.Exists() || completionTokens.Int() != 47 {
		t.Fatalf("completion_tokens = %s, want present with value 47. Output: %s", completionTokens.Raw, result[0])
	}
}

func TestConvertGeminiResponseToOpenAINonStreamCompletionTokensIncludeThoughts(t *testing.T) {
	response := []byte(`{"usageMetadata":{"promptTokenCount":16,"thoughtsTokenCount":42,"totalTokenCount":58}}`)

	result := ConvertGeminiResponseToOpenAINonStream(context.Background(), "model", nil, nil, response, nil)
	completionTokens := ojson.GetBytesNoCopy(result, "usage.completion_tokens")
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
	fr1 := ojson.GetBytesNoCopy(result1[0], "choices.0.finish_reason")
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
	fr3 := ojson.GetBytesNoCopy(result3[0], "choices.0.finish_reason").String()
	if fr3 != "tool_calls" {
		t.Fatalf("expected finish_reason tool_calls, got %s", fr3)
	}
	nfr3 := ojson.GetBytesNoCopy(result3[0], "choices.0.native_finish_reason").String()
	if nfr3 != "stop" {
		t.Fatalf("expected native_finish_reason stop, got %s", nfr3)
	}
}

func TestConvertGeminiResponseToOpenAINonStream_EmptyTextProducesEmptyString(t *testing.T) {
	response := []byte(`{"candidates":[{"content":{"parts":[{"text":""},{"text":"","thought":true}]},"finishReason":"STOP"}]}`)
	result := ConvertGeminiResponseToOpenAINonStream(context.Background(), "model", nil, nil, response, nil)

	content := ojson.GetBytesNoCopy(result, "choices.0.message.content")
	if !content.Exists() || content.String() != "" || content.Type == ojson.Null {
		t.Fatalf("expected content to be empty string \"\", got %v (type %v)", content.Value(), content.Type)
	}

	reasoning := ojson.GetBytesNoCopy(result, "choices.0.message.reasoning_content")
	if !reasoning.Exists() || reasoning.String() != "" || reasoning.Type == ojson.Null {
		t.Fatalf("expected reasoning_content to be empty string \"\", got %v (type %v)", reasoning.Value(), reasoning.Type)
	}
}

// Gemini 3.5 Transcribe returns its transcript in an audioTranscription part instead
// of the regular text part. Without handling it, the transcript is silently dropped
// and the OpenAI client sees an empty assistant message.
func TestConvertGeminiResponseToOpenAINonStream_AudioTranscriptionPartProducesContent(t *testing.T) {
	response := []byte(`{"candidates":[{"content":{"parts":[{"text":""},{"audioTranscription":{"text":"Hello world"}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":185,"totalTokenCount":185}}`)
	result := ConvertGeminiResponseToOpenAINonStream(context.Background(), "model", nil, nil, response, nil)

	content := ojson.GetBytesNoCopy(result, "choices.0.message.content")
	if !content.Exists() || content.String() != "Hello world" {
		t.Fatalf("expected content to carry the transcript, got %v. Output: %s", content.Raw, result)
	}
}

func TestConvertGeminiResponseToOpenAINonStream_SingleAudioTranscriptionPart(t *testing.T) {
	response := []byte(`{"candidates":[{"content":{"parts":[{"audioTranscription":{"text":"Single transcription part"}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":185,"totalTokenCount":185}}`)
	result := ConvertGeminiResponseToOpenAINonStream(context.Background(), "model", nil, nil, response, nil)

	content := ojson.GetBytesNoCopy(result, "choices.0.message.content")
	if !content.Exists() || content.String() != "Single transcription part" {
		t.Fatalf("expected content to carry the transcript, got %v. Output: %s", content.Raw, result)
	}
}

func TestConvertGeminiResponseToOpenAI_AudioTranscriptionPartStreamsContent(t *testing.T) {
	ctx := context.Background()
	var param any

	chunk := []byte(`{"candidates":[{"content":{"parts":[{"text":""},{"audioTranscription":{"text":"Testing one two three."}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":185,"candidatesTokenCount":8,"totalTokenCount":193}}`)
	result := ConvertGeminiResponseToOpenAI(ctx, "model", nil, nil, chunk, &param)
	if len(result) != 2 {
		t.Fatalf("expected 2 results (payload + terminal), got %d", len(result))
	}

	content := ojson.GetBytesNoCopy(result[0], "choices.0.delta.content")
	if !content.Exists() || content.String() != "Testing one two three." {
		t.Fatalf("expected delta.content to carry the transcript, got %v. Output: %s", content.Raw, result[0])
	}
	if got := ojson.GetBytesNoCopy(result[1], "choices.0.finish_reason").String(); got != "stop" {
		t.Fatalf("expected terminal finish_reason stop, got %q. Output: %s", got, result[1])
	}
}

func TestConvertGeminiResponseToOpenAINonStream_TextPrecedenceOverAudioTranscription(t *testing.T) {
	response := []byte(`{"candidates":[{"content":{"parts":[{"text":"explicit text","audioTranscription":{"text":"ignored transcription"}}]},"finishReason":"STOP"}]}`)
	result := ConvertGeminiResponseToOpenAINonStream(context.Background(), "model", nil, nil, response, nil)

	content := ojson.GetBytesNoCopy(result, "choices.0.message.content")
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
	if got := ojson.GetBytesNoCopy(out[0], "choices.0.delta.tool_calls.0.function.name").String(); got != "mcp/search" {
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
	if got := ojson.GetBytesNoCopy(outNil[0], "choices.0.delta.tool_calls.0.function.name").String(); got != "mcp_search" {
		t.Fatalf("tool name = %q, want mcp_search (must not rebuild nil SanitizedNameMap on later chunks)", got)
	}
}

func TestConvertGeminiResponseToOpenAI_NonStopFinishReasonsMapToContentFilter(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name             string
		upstreamReason   string
		wantFinishReason string
		wantNativeReason string
		includeEmptyText bool
	}{
		{
			name:             "MALFORMED_RESPONSE without parts",
			upstreamReason:   "MALFORMED_RESPONSE",
			wantFinishReason: "content_filter",
			wantNativeReason: "malformed_response",
		},
		{
			name:             "MALFORMED_FUNCTION_CALL without parts",
			upstreamReason:   "MALFORMED_FUNCTION_CALL",
			wantFinishReason: "content_filter",
			wantNativeReason: "malformed_function_call",
		},
		{
			name:             "SAFETY",
			upstreamReason:   "SAFETY",
			wantFinishReason: "content_filter",
			wantNativeReason: "safety",
		},
		{
			name:             "RECITATION",
			upstreamReason:   "RECITATION",
			wantFinishReason: "content_filter",
			wantNativeReason: "recitation",
		},
		{
			name:             "PROHIBITED_CONTENT",
			upstreamReason:   "PROHIBITED_CONTENT",
			wantFinishReason: "content_filter",
			wantNativeReason: "prohibited_content",
		},
		{
			name:             "MAX_TOKENS",
			upstreamReason:   "MAX_TOKENS",
			wantFinishReason: "length",
			wantNativeReason: "max_tokens",
		},
	}

	for _, tc := range cases {
		t.Run("stream/"+tc.name, func(t *testing.T) {
			var param any
			chunk := []byte(`{"candidates":[{"content":{"role":"model"},"finishReason":"` + tc.upstreamReason + `"}],"usageMetadata":{"promptTokenCount":481701,"candidatesTokenCount":16,"totalTokenCount":482881,"cachedContentTokenCount":479652,"thoughtsTokenCount":1164},"modelVersion":"gemini-4-argon","responseId":"EAjaaoCDC_Ooq8YP3f-2wAQ"}`)
			out := ConvertGeminiResponseToOpenAI(ctx, "gemini-4-argon", nil, nil, chunk, &param)
			if len(out) != 1 {
				t.Fatalf("expected 1 chunk, got %d", len(out))
			}
			if got := ojson.GetBytesNoCopy(out[0], "choices.0.finish_reason").String(); got != tc.wantFinishReason {
				t.Fatalf("stream finish_reason = %q, want %q; payload=%s", got, tc.wantFinishReason, out[0])
			}
			if got := ojson.GetBytesNoCopy(out[0], "choices.0.native_finish_reason").String(); got != tc.wantNativeReason {
				t.Fatalf("stream native_finish_reason = %q, want %q; payload=%s", got, tc.wantNativeReason, out[0])
			}
		})

		t.Run("nonstream/"+tc.name, func(t *testing.T) {
			raw := []byte(`{"candidates":[{"content":{"role":"model"},"finishReason":"` + tc.upstreamReason + `"}],"usageMetadata":{"promptTokenCount":481701,"candidatesTokenCount":16,"totalTokenCount":482881},"modelVersion":"gemini-4-argon","responseId":"EAjaaoCDC_Ooq8YP3f-2wAQ"}`)
			out := ConvertGeminiResponseToOpenAINonStream(ctx, "gemini-4-argon", nil, nil, raw, nil)
			if got := ojson.GetBytesNoCopy(out, "choices.0.finish_reason").String(); got != tc.wantFinishReason {
				t.Fatalf("non-stream finish_reason = %q, want %q; payload=%s", got, tc.wantFinishReason, out)
			}
			if got := ojson.GetBytesNoCopy(out, "choices.0.native_finish_reason").String(); got != tc.wantNativeReason {
				t.Fatalf("non-stream native_finish_reason = %q, want %q; payload=%s", got, tc.wantNativeReason, out)
			}
		})
	}
}

func TestConvertGeminiResponseToOpenAI_FinishReasonWithoutInlineUsage(t *testing.T) {
	ctx := context.Background()

	t.Run("finishReason chunk followed by standalone usage chunk", func(t *testing.T) {
		var param any
		chunk1 := []byte(`{"candidates":[{"content":{"role":"model"},"finishReason":"MALFORMED_RESPONSE"}],"modelVersion":"gemini-4-argon","responseId":"resp-split-1"}`)
		chunk2 := []byte(`{"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":5,"totalTokenCount":105},"modelVersion":"gemini-4-argon","responseId":"resp-split-1"}`)

		out1 := ConvertGeminiResponseToOpenAI(ctx, "gemini-4-argon", nil, nil, chunk1, &param)
		out2 := ConvertGeminiResponseToOpenAI(ctx, "gemini-4-argon", nil, nil, chunk2, &param)
		done := ConvertGeminiResponseToOpenAI(ctx, "gemini-4-argon", nil, nil, []byte("[DONE]"), &param)

		var all [][]byte
		all = append(all, out1...)
		all = append(all, out2...)
		all = append(all, done...)

		var terminals [][]byte
		for _, c := range all {
			if ojson.GetBytesNoCopy(c, "choices.0.finish_reason").Value() != nil {
				terminals = append(terminals, c)
			}
		}
		if len(terminals) != 1 {
			t.Fatalf("expected exactly 1 chunk with non-null finish_reason, got %d; chunks=%q", len(terminals), all)
		}
		terminal := terminals[0]
		if got := ojson.GetBytesNoCopy(terminal, "choices.0.finish_reason").String(); got != "content_filter" {
			t.Fatalf("finish_reason = %q, want content_filter; payload=%s", got, terminal)
		}
		if got := ojson.GetBytesNoCopy(terminal, "choices.0.native_finish_reason").String(); got != "malformed_response" {
			t.Fatalf("native_finish_reason = %q, want malformed_response; payload=%s", got, terminal)
		}
		if got := ojson.GetBytesNoCopy(terminal, "usage.prompt_tokens").Int(); got != 100 {
			t.Fatalf("usage.prompt_tokens = %d, want 100; payload=%s", got, terminal)
		}
	})

	t.Run("finishReason chunk with cpaUsageMetadata finalized on DONE", func(t *testing.T) {
		var param any
		chunk1 := []byte(`{"candidates":[{"content":{"role":"model"},"finishReason":"MALFORMED_RESPONSE"}],"cpaUsageMetadata":{"promptTokenCount":200,"candidatesTokenCount":8,"totalTokenCount":208},"modelVersion":"gemini-4-argon","responseId":"resp-done-1"}`)

		out1 := ConvertGeminiResponseToOpenAI(ctx, "gemini-4-argon", nil, nil, chunk1, &param)
		done := ConvertGeminiResponseToOpenAI(ctx, "gemini-4-argon", nil, nil, []byte("[DONE]"), &param)

		var all [][]byte
		all = append(all, out1...)
		all = append(all, done...)

		var terminals [][]byte
		for _, c := range all {
			if ojson.GetBytesNoCopy(c, "choices.0.finish_reason").Value() != nil {
				terminals = append(terminals, c)
			}
		}
		if len(terminals) != 1 {
			t.Fatalf("expected exactly 1 chunk with non-null finish_reason on [DONE], got %d; chunks=%q", len(terminals), all)
		}
		terminal := terminals[0]
		if got := ojson.GetBytesNoCopy(terminal, "choices.0.finish_reason").String(); got != "content_filter" {
			t.Fatalf("finish_reason = %q, want content_filter; payload=%s", got, terminal)
		}
		if got := ojson.GetBytesNoCopy(terminal, "choices.0.native_finish_reason").String(); got != "malformed_response" {
			t.Fatalf("native_finish_reason = %q, want malformed_response; payload=%s", got, terminal)
		}
		if got := ojson.GetBytesNoCopy(terminal, "usage.prompt_tokens").Int(); got != 200 {
			t.Fatalf("usage.prompt_tokens = %d, want 200; payload=%s", got, terminal)
		}
		if extraDone := ConvertGeminiResponseToOpenAI(ctx, "gemini-4-argon", nil, nil, []byte("[DONE]"), &param); len(extraDone) != 0 {
			t.Fatalf("expected repeated [DONE] to emit no chunks, got %q", extraDone)
		}
	})

	t.Run("finishReason chunk without any usage finalized on DONE", func(t *testing.T) {
		var param any
		chunk1 := []byte(`{"candidates":[{"content":{"role":"model"},"finishReason":"SAFETY"}],"modelVersion":"gemini-4-argon","responseId":"resp-nousage-1"}`)

		out1 := ConvertGeminiResponseToOpenAI(ctx, "gemini-4-argon", nil, nil, chunk1, &param)
		done := ConvertGeminiResponseToOpenAI(ctx, "gemini-4-argon", nil, nil, []byte("[DONE]"), &param)
		if len(done) != 1 {
			t.Fatalf("expected 1 terminal chunk on [DONE], got %d (out1=%q done=%q)", len(done), out1, done)
		}
		if got := ojson.GetBytesNoCopy(done[0], "choices.0.finish_reason").String(); got != "content_filter" {
			t.Fatalf("finish_reason = %q, want content_filter; payload=%s", got, done[0])
		}
		if got := ojson.GetBytesNoCopy(done[0], "choices.0.native_finish_reason").String(); got != "safety" {
			t.Fatalf("native_finish_reason = %q, want safety; payload=%s", got, done[0])
		}
	})
}

func TestConvertGeminiResponseToOpenAI_ParallelToolCallsAcrossChunks(t *testing.T) {
	ctx := context.Background()
	var param any
	chunk1 := []byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"id":"call_utc","name":"get_time","args":{"tz":"UTC"}}}]}}],"modelVersion":"gemini-3-flash","responseId":"resp-parallel-1"}`)
	chunk2 := []byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"id":"call_scl","name":"get_time","args":{"tz":"America/Santiago"}}},{"functionCall":{"id":"call_tyo","name":"get_time","args":{"tz":"Asia/Tokyo"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"gemini-3-flash","responseId":"resp-parallel-1"}`)

	var indices []int64
	var ids []string
	for _, chunk := range [][]byte{chunk1, chunk2} {
		for _, out := range ConvertGeminiResponseToOpenAI(ctx, "gemini-3-flash", nil, nil, chunk, &param) {
			for _, tc := range ojson.GetBytesNoCopy(out, "choices.0.delta.tool_calls").Array() {
				indices = append(indices, tc.Get("index").Int())
				ids = append(ids, tc.Get("id").String())
			}
		}
	}
	if len(indices) != 3 || indices[0] != 0 || indices[1] != 1 || indices[2] != 2 {
		t.Fatalf("parallel tool call indices across chunks = %v, want [0 1 2]", indices)
	}
	if len(ids) != 3 || ids[0] != "call_utc" || ids[1] != "call_scl" || ids[2] != "call_tyo" {
		t.Fatalf("parallel tool call ids across chunks = %v, want [call_utc call_scl call_tyo]", ids)
	}
}

func TestConvertGeminiResponseToOpenAI_VertexStreamingPartialArgs(t *testing.T) {
	ctx := context.Background()
	var param any
	chunks := [][]byte{
		[]byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","partialArgs":[{"jsonPath":"$.city","stringValue":"New ","willContinue":true}],"willContinue":true}}]}}],"modelVersion":"gemini-2.5-pro","responseId":"resp-partial-1"}`),
		[]byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"partialArgs":[{"jsonPath":"$.city","stringValue":"York"}],"willContinue":true}}]}}],"modelVersion":"gemini-2.5-pro","responseId":"resp-partial-1"}`),
		[]byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"gemini-2.5-pro","responseId":"resp-partial-1"}`),
	}

	var (
		callID       string
		callName     string
		argsBuilder  strings.Builder
		firstChunkOK bool
	)
	for chunkIdx, chunk := range chunks {
		outs := ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-pro", nil, nil, chunk, &param)
		for _, out := range outs {
			tcs := ojson.GetBytesNoCopy(out, "choices.0.delta.tool_calls")
			if !tcs.Exists() || !tcs.IsArray() {
				continue
			}
			for _, tc := range tcs.Array() {
				if gotIdx := tc.Get("index").Int(); gotIdx != 0 {
					t.Fatalf("chunk %d tool_call index = %d, want 0; payload=%s", chunkIdx, gotIdx, out)
				}
				if id := tc.Get("id").String(); id != "" {
					if callID != "" && callID != id {
						t.Fatalf("chunk %d changed tool_call id from %q to %q", chunkIdx, callID, id)
					}
					callID = id
				}
				if name := tc.Get("function.name").String(); name != "" {
					callName = name
				}
				argDelta := tc.Get("function.arguments").String()
				argsBuilder.WriteString(argDelta)
				if chunkIdx == 0 && strings.Contains(argDelta, "New ") {
					firstChunkOK = true
				}
			}
		}
	}
	if !firstChunkOK {
		t.Fatalf("expected first chunk to stream 'New ' immediately, got total args %q", argsBuilder.String())
	}
	if callName != "get_weather" {
		t.Fatalf("tool call name = %q, want get_weather", callName)
	}
	if got := argsBuilder.String(); got != `{"city":"New York"}` {
		t.Fatalf("concatenated arguments = %q, want %q", got, `{"city":"New York"}`)
	}

	t.Run("multiple same-name calls and complex types", func(t *testing.T) {
		var p any
		streamChunks := [][]byte{
			[]byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","partialArgs":[{"jsonPath":"$.city","stringValue":"Tok","willContinue":true}],"willContinue":true}}]}}],"modelVersion":"gemini-2.5-pro","responseId":"resp-multi"}`),
			[]byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"partialArgs":[{"jsonPath":"$.city","stringValue":"yo","willContinue":true}],"willContinue":true}}]}}],"modelVersion":"gemini-2.5-pro","responseId":"resp-multi"}`),
			[]byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"partialArgs":[{"jsonPath":"$.city"},{"jsonPath":"$.days","numberValue":0},{"jsonPath":"$.metric","boolValue":false},{"jsonPath":"$.extra","nullValue":"NULL_VALUE"}],"willContinue":true}}]}}],"modelVersion":"gemini-2.5-pro","responseId":"resp-multi"}`),
			[]byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{}}]}}],"modelVersion":"gemini-2.5-pro","responseId":"resp-multi"}`),
			[]byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","partialArgs":[{"jsonPath":"$.city","stringValue":"Os","willContinue":true}],"willContinue":true}}]}}],"modelVersion":"gemini-2.5-pro","responseId":"resp-multi"}`),
			[]byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"partialArgs":[{"jsonPath":"$.city","stringValue":"aka"}]}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"gemini-2.5-pro","responseId":"resp-multi"}`),
		}

		idsByIndex := make(map[int64]string)
		argsByIndex := make(map[int64]*strings.Builder)
		var finishReason string
		for _, ch := range streamChunks {
			for _, out := range ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-pro", nil, nil, ch, &p) {
				if fr := ojson.GetBytesNoCopy(out, "choices.0.finish_reason"); fr.Exists() && fr.Type != ojson.Null {
					finishReason = fr.String()
				}
				for _, tc := range ojson.GetBytesNoCopy(out, "choices.0.delta.tool_calls").Array() {
					idx := tc.Get("index").Int()
					if id := tc.Get("id").String(); id != "" {
						idsByIndex[idx] = id
					}
					if argsByIndex[idx] == nil {
						argsByIndex[idx] = &strings.Builder{}
					}
					argsByIndex[idx].WriteString(tc.Get("function.arguments").String())
				}
			}
		}
		if len(idsByIndex) != 2 || idsByIndex[0] == "" || idsByIndex[1] == "" || idsByIndex[0] == idsByIndex[1] {
			t.Fatalf("expected 2 distinct tool call IDs at indices 0 and 1, got %v", idsByIndex)
		}
		if got0 := argsByIndex[0].String(); got0 != `{"city":"Tokyo","days":0,"metric":false,"extra":null}` {
			t.Fatalf("call 0 args = %q", got0)
		}
		if got1 := argsByIndex[1].String(); got1 != `{"city":"Osaka"}` {
			t.Fatalf("call 1 args = %q", got1)
		}
		if finishReason != "tool_calls" {
			t.Fatalf("finish_reason = %q, want tool_calls", finishReason)
		}
	})

	t.Run("single-chunk willContinue false parameterless call and empty partialArgs with complete args", func(t *testing.T) {
		var p1 any
		cPing := []byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"name":"ping","willContinue":false}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2,"totalTokenCount":7},"modelVersion":"gemini-2.5-pro","responseId":"resp-ping"}`)
		outs1 := ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-pro", nil, nil, cPing, &p1)
		if got := ojson.GetBytesNoCopy(outs1[0], "choices.0.delta.tool_calls.0.function.arguments").String(); got != "{}" {
			t.Fatalf("ping arguments = %q, want {}", got)
		}

		var p2 any
		cArgs := []byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"Paris"},"partialArgs":[]}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2,"totalTokenCount":7},"modelVersion":"gemini-2.5-pro","responseId":"resp-args"}`)
		outs2 := ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-pro", nil, nil, cArgs, &p2)
		if got := ojson.GetBytesNoCopy(outs2[0], "choices.0.delta.tool_calls.0.function.arguments").String(); got != `{"city":"Paris"}` {
			t.Fatalf("args fallback = %q", got)
		}
	})

	t.Run("continuation chunk repeating tool name without id continues active call", func(t *testing.T) {
		var p any
		chunks := [][]byte{
			[]byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"get_weather","partialArgs":[{"jsonPath":"$.city","stringValue":"New ","willContinue":true}],"willContinue":true}}]}}],"modelVersion":"gemini-2.5-pro","responseId":"resp-repeat-name"}`),
			[]byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","partialArgs":[{"jsonPath":"$.city","stringValue":"York"}],"willContinue":false}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"gemini-2.5-pro","responseId":"resp-repeat-name"}`),
			[]byte("[DONE]"),
		}
		var argBuf strings.Builder
		var finishReason string
		var idCount int
		for _, ch := range chunks {
			for _, out := range ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-pro", nil, nil, ch, &p) {
				for _, tc := range ojson.GetBytesNoCopy(out, "choices.0.delta.tool_calls").Array() {
					if tc.Get("id").String() != "" {
						idCount++
					}
					argBuf.WriteString(tc.Get("function.arguments").String())
				}
				if fr := ojson.GetBytesNoCopy(out, "choices.0.finish_reason"); fr.Exists() && fr.Type != ojson.Null {
					finishReason = fr.String()
				}
			}
		}
		if idCount != 1 {
			t.Fatalf("expected 1 tool call start, got %d", idCount)
		}
		if got := argBuf.String(); got != `{"city":"New York"}` {
			t.Fatalf("arguments = %q, want %q", got, `{"city":"New York"}`)
		}
		if finishReason != "tool_calls" {
			t.Fatalf("finish_reason = %q, want tool_calls", finishReason)
		}
	})

	t.Run("mid-stream empty text and thoughtSignature do not interrupt active call", func(t *testing.T) {
		var p any
		chunks := [][]byte{
			[]byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","partialArgs":[{"jsonPath":"$.city","stringValue":"Ber","willContinue":true}],"willContinue":true}}]}}],"modelVersion":"gemini-2.5-pro","responseId":"resp-mid-sig"}`),
			[]byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"","thoughtSignature":"sig-mid"}]}}],"modelVersion":"gemini-2.5-pro","responseId":"resp-mid-sig"}`),
			[]byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"partialArgs":[{"jsonPath":"$.city","stringValue":"lin"}],"willContinue":true}}]}}],"modelVersion":"gemini-2.5-pro","responseId":"resp-mid-sig"}`),
			[]byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"gemini-2.5-pro","responseId":"resp-mid-sig"}`),
			[]byte("[DONE]"),
		}
		var argBuf strings.Builder
		var finishReason string
		for _, ch := range chunks {
			for _, out := range ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-pro", nil, nil, ch, &p) {
				for _, tc := range ojson.GetBytesNoCopy(out, "choices.0.delta.tool_calls").Array() {
					argBuf.WriteString(tc.Get("function.arguments").String())
				}
				if fr := ojson.GetBytesNoCopy(out, "choices.0.finish_reason"); fr.Exists() && fr.Type != ojson.Null {
					finishReason = fr.String()
				}
			}
		}
		if got := argBuf.String(); got != `{"city":"Berlin"}` {
			t.Fatalf("arguments = %q, want %q", got, `{"city":"Berlin"}`)
		}
		if finishReason != "tool_calls" {
			t.Fatalf("finish_reason = %q, want tool_calls", finishReason)
		}
	})

	t.Run("replaced unfinished call does not fabricate completion", func(t *testing.T) {
		var p any
		c1 := []byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"id":"call_a","name":"get_weather","partialArgs":[{"jsonPath":"$.city","stringValue":"New ","willContinue":true}],"willContinue":true}}]}}],"modelVersion":"gemini-2.5-pro","responseId":"resp-replace"}`)
		c2 := []byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"id":"call_b","name":"get_weather","partialArgs":[{"jsonPath":"$.city","stringValue":"LA"}],"willContinue":false}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"gemini-2.5-pro","responseId":"resp-replace"}`)
		var all [][]byte
		all = append(all, ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-pro", nil, nil, c1, &p)...)
		all = append(all, ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-pro", nil, nil, c2, &p)...)
		all = append(all, ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-pro", nil, nil, []byte("[DONE]"), &p)...)
		for _, out := range all {
			if fr := ojson.GetBytesNoCopy(out, "choices.0.finish_reason"); fr.Exists() && fr.Type != ojson.Null {
				t.Fatalf("unexpected finish_reason when call_a never completed: %s", out)
			}
		}
	})

	t.Run("interrupted stream does not fabricate completion", func(t *testing.T) {
		var p any
		incomplete := []byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","partialArgs":[{"jsonPath":"$.city","stringValue":"New ","willContinue":true}],"willContinue":true}}]},"finishReason":"STOP"}],"modelVersion":"gemini-2.5-pro","responseId":"resp-trunc"}`)
		outs := ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-pro", nil, nil, incomplete, &p)
		doneOuts := ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-pro", nil, nil, []byte("[DONE]"), &p)
		all := append(outs, doneOuts...)
		for _, out := range all {
			if fr := ojson.GetBytesNoCopy(out, "choices.0.finish_reason"); fr.Exists() && fr.Type != ojson.Null {
				t.Fatalf("unexpected finish_reason on interrupted tool call stream: %s", out)
			}
		}
	})
}

func TestConvertGeminiResponseToOpenAI_InputBufferReuse(t *testing.T) {
	ctx := context.Background()
	var param any
	buf := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"modelVersion":"gemini-2.5-flash","responseId":"resp_buf_1"}`)
	ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-flash", nil, nil, buf, &param)
	for i := range buf {
		buf[i] = 'X'
	}
	done := ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-flash", nil, nil, []byte("[DONE]"), &param)
	if len(done) != 1 {
		t.Fatalf("expected 1 terminal chunk on [DONE], got %d", len(done))
	}
	if got := ojson.GetBytesNoCopy(done[0], "id").String(); got != "resp_buf_1" {
		t.Fatalf("id = %q, want resp_buf_1", got)
	}
	if got := ojson.GetBytesNoCopy(done[0], "model").String(); got != "gemini-2.5-flash" {
		t.Fatalf("model = %q, want gemini-2.5-flash", got)
	}
	if got := ojson.GetBytesNoCopy(done[0], "choices.0.finish_reason").String(); got != "stop" {
		t.Fatalf("finish_reason = %q, want stop; payload=%s", got, done[0])
	}
}

func TestConvertGeminiResponseToOpenAI_MultiPartThoughtAndTrailingSignature(t *testing.T) {
	ctx := context.Background()

	t.Run("thought followed by empty-text thoughtSignature part", func(t *testing.T) {
		var param any
		chunk := []byte(`{"candidates":[{"content":{"parts":[{"thought":true,"text":"Analyzing the request..."},{"thought":true,"text":"","thoughtSignature":"EjQK..."}]}}]}`)
		out := ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-pro", nil, nil, chunk, &param)
		if len(out) != 1 {
			t.Fatalf("expected 1 chunk, got %d", len(out))
		}
		if got := ojson.GetBytesNoCopy(out[0], "choices.0.delta.reasoning_content").String(); got != "Analyzing the request..." {
			t.Fatalf("delta.reasoning_content = %q, want %q; payload=%s", got, "Analyzing the request...", out[0])
		}
		if content := ojson.GetBytesNoCopy(out[0], "choices.0.delta.content"); content.Type != ojson.Null {
			t.Fatalf("delta.content = %s (type %v), want null; payload=%s", content.Raw, content.Type, out[0])
		}
		if role := ojson.GetBytesNoCopy(out[0], "choices.0.delta.role").String(); role != "assistant" {
			t.Fatalf("delta.role = %q, want assistant; payload=%s", role, out[0])
		}
	})

	t.Run("thought followed by non-thought empty-text thoughtSignature part", func(t *testing.T) {
		var param any
		chunk := []byte(`{"candidates":[{"content":{"parts":[{"thought":true,"text":"Analyzing the request..."},{"text":"","thoughtSignature":"EjQK..."}]}}]}`)
		out := ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-pro", nil, nil, chunk, &param)
		if len(out) != 1 {
			t.Fatalf("expected 1 chunk, got %d", len(out))
		}
		if got := ojson.GetBytesNoCopy(out[0], "choices.0.delta.reasoning_content").String(); got != "Analyzing the request..." {
			t.Fatalf("delta.reasoning_content = %q, want %q; payload=%s", got, "Analyzing the request...", out[0])
		}
		if content := ojson.GetBytesNoCopy(out[0], "choices.0.delta.content"); content.Type != ojson.Null {
			t.Fatalf("delta.content = %s (type %v), want null; payload=%s", content.Raw, content.Type, out[0])
		}
	})

	t.Run("visible text followed by empty-text thoughtSignature part", func(t *testing.T) {
		var param any
		chunk := []byte(`{"candidates":[{"content":{"parts":[{"text":"Final answer"},{"text":"","thoughtSignature":"EjQK..."}]}}]}`)
		out := ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-pro", nil, nil, chunk, &param)
		if len(out) != 1 {
			t.Fatalf("expected 1 chunk, got %d", len(out))
		}
		if got := ojson.GetBytesNoCopy(out[0], "choices.0.delta.content").String(); got != "Final answer" {
			t.Fatalf("delta.content = %q, want %q; payload=%s", got, "Final answer", out[0])
		}
		if reasoning := ojson.GetBytesNoCopy(out[0], "choices.0.delta.reasoning_content"); reasoning.Type != ojson.Null {
			t.Fatalf("delta.reasoning_content = %s (type %v), want null; payload=%s", reasoning.Raw, reasoning.Type, out[0])
		}
	})

	t.Run("multiple thought parts and multiple text parts in same chunk", func(t *testing.T) {
		var param any
		chunk := []byte(`{"candidates":[{"content":{"parts":[{"thought":true,"text":"Step 1. "},{"thought":true,"text":"Step 2."},{"text":"Hello "},{"text":"world"}]}}]}`)
		out := ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-pro", nil, nil, chunk, &param)
		if len(out) != 1 {
			t.Fatalf("expected 1 chunk, got %d", len(out))
		}
		if got := ojson.GetBytesNoCopy(out[0], "choices.0.delta.reasoning_content").String(); got != "Step 1. Step 2." {
			t.Fatalf("delta.reasoning_content = %q, want %q; payload=%s", got, "Step 1. Step 2.", out[0])
		}
		if got := ojson.GetBytesNoCopy(out[0], "choices.0.delta.content").String(); got != "Hello world" {
			t.Fatalf("delta.content = %q, want %q; payload=%s", got, "Hello world", out[0])
		}
	})

	t.Run("non-stream pure thoughtSignature carrier with empty text does not emit empty string", func(t *testing.T) {
		raw := []byte(`{"candidates":[{"content":{"parts":[{"thought":true,"text":"Thinking..."},{"text":"","thoughtSignature":"EjQK..."},{"functionCall":{"name":"get_time","args":{"tz":"UTC"}}}]},"finishReason":"STOP"}]}`)
		out := ConvertGeminiResponseToOpenAINonStream(ctx, "gemini-2.5-pro", nil, nil, raw, nil)
		if got := ojson.GetBytesNoCopy(out, "choices.0.message.reasoning_content").String(); got != "Thinking..." {
			t.Fatalf("message.reasoning_content = %q, want %q; payload=%s", got, "Thinking...", out)
		}
		if content := ojson.GetBytesNoCopy(out, "choices.0.message.content"); content.Type != ojson.Null {
			t.Fatalf("message.content = %s (type %v), want null; payload=%s", content.Raw, content.Type, out)
		}
	})
}

func TestConvertGeminiResponseToOpenAINonStream_SkipsThoughtInlineDataImages(t *testing.T) {
	ctx := context.Background()
	raw := []byte(`{
		"candidates": [{
			"index": 0,
			"content": {
				"role": "model",
				"parts": [
					{"thought": true, "text": "**Rendering objects realistically** I'm focusing on the apple."},
					{"thought": true, "inlineData": {"mimeType": "image/png", "data": "RFJBRlRfSU1BR0U="}},
					{"thought": true, "text": "**Refining Image Details** Adjusting highlights."},
					{"inlineData": {"mimeType": "image/png", "data": "RklOQUxfSU1BR0U="}, "thoughtSignature": "EjQKDg=="}
				]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {
			"promptTokenCount": 12,
			"candidatesTokenCount": 1120,
			"thoughtsTokenCount": 584,
			"totalTokenCount": 1716
		}
	}`)

	out := ConvertGeminiResponseToOpenAINonStream(ctx, "gemini-3-pro-image", nil, nil, raw, nil)

	if content := ojson.GetBytesNoCopy(out, "choices.0.message.content"); content.Type != ojson.Null {
		t.Fatalf("message.content = %s (type %v), want null; payload=%s", content.Raw, content.Type, out)
	}
	wantReasoning := "**Rendering objects realistically** I'm focusing on the apple.**Refining Image Details** Adjusting highlights."
	if got := ojson.GetBytesNoCopy(out, "choices.0.message.reasoning_content").String(); got != wantReasoning {
		t.Fatalf("message.reasoning_content = %q, want %q; payload=%s", got, wantReasoning, out)
	}
	images := ojson.GetBytesNoCopy(out, "choices.0.message.images").Array()
	if len(images) != 1 {
		t.Fatalf("len(message.images) = %d, want 1; payload=%s", len(images), out)
	}
	if got := images[0].Get("index").Int(); got != 0 {
		t.Fatalf("message.images[0].index = %d, want 0; payload=%s", got, out)
	}
	if got := images[0].Get("image_url.url").String(); got != "data:image/png;base64,RklOQUxfSU1BR0U=" {
		t.Fatalf("message.images[0].image_url.url = %q, want deliverable image URL; payload=%s", got, out)
	}
}

func TestConvertGeminiResponseToOpenAI_SkipsThoughtInlineDataImages(t *testing.T) {
	ctx := context.Background()

	t.Run("multi-frame stream drops thought image frame without null delta", func(t *testing.T) {
		var param any

		chunk0 := []byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"thought":true,"text":"**Rendering objects realistically** "}]}}],"cpaUsageMetadata":{"promptTokenCount":12},"modelVersion":"gemini-3-pro-image","responseId":"resp_7566"}`)
		out0 := ConvertGeminiResponseToOpenAI(ctx, "gemini-3-pro-image", nil, nil, chunk0, &param)
		if len(out0) != 1 {
			t.Fatalf("chunk0 count = %d, want 1", len(out0))
		}
		if ojson.GetBytesNoCopy(out0[0], "choices.0.delta.images").Exists() {
			t.Fatalf("unexpected delta.images in chunk0: %s", out0[0])
		}

		chunk1 := []byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"thought":true,"inlineData":{"mimeType":"image/png","data":"RFJBRlRfSU1BR0U="}}]}}],"cpaUsageMetadata":{"promptTokenCount":12},"modelVersion":"gemini-3-pro-image","responseId":"resp_7566"}`)
		out1 := ConvertGeminiResponseToOpenAI(ctx, "gemini-3-pro-image", nil, nil, chunk1, &param)
		if len(out1) != 0 {
			t.Fatalf("standalone thought inlineData chunk must not emit delta.images or all-null delta, got %d chunk(s): %s", len(out1), out1[0])
		}

		chunk2 := []byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"thought":true,"text":"**Refining Image Details**"}]}}],"cpaUsageMetadata":{"promptTokenCount":12},"modelVersion":"gemini-3-pro-image","responseId":"resp_7566"}`)
		out2 := ConvertGeminiResponseToOpenAI(ctx, "gemini-3-pro-image", nil, nil, chunk2, &param)
		if len(out2) != 1 {
			t.Fatalf("chunk2 count = %d, want 1", len(out2))
		}

		chunk3 := []byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"RklOQUxfSU1BR0U="},"thoughtSignature":"EjQKDg=="}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":1120,"thoughtsTokenCount":584,"totalTokenCount":1716},"modelVersion":"gemini-3-pro-image","responseId":"resp_7566"}`)
		out3 := ConvertGeminiResponseToOpenAI(ctx, "gemini-3-pro-image", nil, nil, chunk3, &param)
		if len(out3) != 2 {
			t.Fatalf("chunk3 count = %d, want 2 (payload + terminal)", len(out3))
		}
		images := ojson.GetBytesNoCopy(out3[0], "choices.0.delta.images").Array()
		if len(images) != 1 {
			t.Fatalf("len(delta.images) = %d, want 1; payload=%s", len(images), out3[0])
		}
		if got := images[0].Get("index").Int(); got != 0 {
			t.Fatalf("delta.images[0].index = %d, want 0; payload=%s", got, out3[0])
		}
		if got := images[0].Get("image_url.url").String(); got != "data:image/png;base64,RklOQUxfSU1BR0U=" {
			t.Fatalf("delta.images[0].image_url.url = %q, want deliverable image URL; payload=%s", got, out3[0])
		}
		if got := ojson.GetBytesNoCopy(out3[1], "choices.0.finish_reason").String(); got != "stop" {
			t.Fatalf("finish_reason = %q, want stop; payload=%s", got, out3[1])
		}
	})

	t.Run("single-frame multi-part candidate emits only non-thought image at index 0", func(t *testing.T) {
		var param any
		chunk := []byte(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"thought":true,"text":"Drafting. "},{"thought":true,"inline_data":{"mime_type":"image/png","data":"RFJBRlRfSU1BR0U="}},{"thought":true,"text":"Refining."},{"inlineData":{"mimeType":"image/png","data":"RklOQUxfSU1BR0U="}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":100,"thoughtsTokenCount":50,"totalTokenCount":160}}`)
		out := ConvertGeminiResponseToOpenAI(ctx, "gemini-nano-banana-2.1", nil, nil, chunk, &param)
		if len(out) != 2 {
			t.Fatalf("chunk count = %d, want 2 (payload + terminal)", len(out))
		}
		if got := ojson.GetBytesNoCopy(out[0], "choices.0.delta.reasoning_content").String(); got != "Drafting. Refining." {
			t.Fatalf("delta.reasoning_content = %q, want %q; payload=%s", got, "Drafting. Refining.", out[0])
		}
		images := ojson.GetBytesNoCopy(out[0], "choices.0.delta.images").Array()
		if len(images) != 1 {
			t.Fatalf("len(delta.images) = %d, want 1; payload=%s", len(images), out[0])
		}
		if got := images[0].Get("index").Int(); got != 0 {
			t.Fatalf("delta.images[0].index = %d, want 0; payload=%s", got, out[0])
		}
		if got := images[0].Get("image_url.url").String(); got != "data:image/png;base64,RklOQUxfSU1BR0U=" {
			t.Fatalf("delta.images[0].image_url.url = %q, want deliverable image URL; payload=%s", got, out[0])
		}
		if got := ojson.GetBytesNoCopy(out[1], "choices.0.finish_reason").String(); got != "stop" {
			t.Fatalf("finish_reason = %q, want stop; payload=%s", got, out[1])
		}
	})
}

func TestConvertGeminiResponseToOpenAI_FunctionCallWithoutArgsUsesEmptyObject(t *testing.T) {
	upstream := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"get_time"}}]},"finishReason":"STOP"}]}`)

	t.Run("streaming", func(t *testing.T) {
		var param any
		outputs := ConvertGeminiResponseToOpenAI(context.Background(), "gemini-2.5-flash", nil, nil, upstream, &param)
		if len(outputs) != 1 {
			t.Fatalf("output count = %d, want 1", len(outputs))
		}
		arguments := ojson.GetBytesNoCopy(outputs[0], "choices.0.delta.tool_calls.0.function.arguments")
		if arguments.Type != ojson.String || arguments.String() != "{}" {
			t.Fatalf("function.arguments = %s, want JSON string %q; payload=%s", arguments.Raw, "{}", outputs[0])
		}
	})

	t.Run("non-streaming", func(t *testing.T) {
		output := ConvertGeminiResponseToOpenAINonStream(context.Background(), "gemini-2.5-flash", nil, nil, upstream, nil)
		arguments := ojson.GetBytesNoCopy(output, "choices.0.message.tool_calls.0.function.arguments")
		if arguments.Type != ojson.String || arguments.String() != "{}" {
			t.Fatalf("function.arguments = %s, want JSON string %q; payload=%s", arguments.Raw, "{}", output)
		}
	})
}

func TestConvertGeminiResponseToOpenAI_RestoresSanitizedOpenAIToolNames(t *testing.T) {
	ctx := context.Background()
	originalReq := []byte(`{"model":"gemini-4-argon","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"mcp/search","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}}]}`)
	upstream := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"mcp_search","args":{"q":"test"}}}]},"finishReason":"STOP"}]}`)

	t.Run("streaming", func(t *testing.T) {
		var param any
		outputs := ConvertGeminiResponseToOpenAI(ctx, "gemini-4-argon", originalReq, nil, upstream, &param)
		if len(outputs) != 1 {
			t.Fatalf("output count = %d, want 1", len(outputs))
		}
		if got := ojson.GetBytesNoCopy(outputs[0], "choices.0.delta.tool_calls.0.function.name").String(); got != "mcp/search" {
			t.Fatalf("function.name = %q, want %q; payload=%s", got, "mcp/search", outputs[0])
		}
	})

	t.Run("streaming vertex partial args", func(t *testing.T) {
		var param any
		partial := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"mcp_search","willContinue":true,"partialArgs":[{"jsonPath":"$.q","stringValue":"test"}]}}]}}]}`)
		outputs := ConvertGeminiResponseToOpenAI(ctx, "gemini-4-argon", originalReq, nil, partial, &param)
		if len(outputs) != 1 {
			t.Fatalf("output count = %d, want 1", len(outputs))
		}
		if got := ojson.GetBytesNoCopy(outputs[0], "choices.0.delta.tool_calls.0.function.name").String(); got != "mcp/search" {
			t.Fatalf("function.name = %q, want %q; payload=%s", got, "mcp/search", outputs[0])
		}
	})

	t.Run("streaming reused param lazy init", func(t *testing.T) {
		var param any = &convertGeminiResponseToOpenAIChatParams{}
		outputs := ConvertGeminiResponseToOpenAI(ctx, "gemini-4-argon", originalReq, nil, upstream, &param)
		if len(outputs) != 1 {
			t.Fatalf("output count = %d, want 1", len(outputs))
		}
		if got := ojson.GetBytesNoCopy(outputs[0], "choices.0.delta.tool_calls.0.function.name").String(); got != "mcp/search" {
			t.Fatalf("function.name = %q, want %q; payload=%s", got, "mcp/search", outputs[0])
		}
	})

	t.Run("non-streaming", func(t *testing.T) {
		output := ConvertGeminiResponseToOpenAINonStream(ctx, "gemini-4-argon", originalReq, nil, upstream, nil)
		if got := ojson.GetBytesNoCopy(output, "choices.0.message.tool_calls.0.function.name").String(); got != "mcp/search" {
			t.Fatalf("function.name = %q, want %q; payload=%s", got, "mcp/search", output)
		}
	})

	t.Run("allowed_tools excludes colliding sibling round-trip", func(t *testing.T) {
		filteredReq := []byte(`{
			"model":"gemini-4-argon",
			"messages":[{"role":"user","content":"hi"}],
			"tools":[
				{"type":"function","function":{"name":"mcp/search","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}},
				{"type":"function","function":{"name":"mcp_search","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}}
			],
			"tool_choice":{"type":"allowed_tools","allowed_tools":{"mode":"auto","tools":[{"type":"function","function":{"name":"mcp/search"}}]}}
		}`)
		geminiReq, errConv := ConvertOpenAIRequestToGemini("gemini-4-argon", filteredReq, false)
		if errConv != nil {
			t.Fatalf("ConvertOpenAIRequestToGemini unexpected error: %v", errConv)
		}
		decls := ojson.GetBytesNoCopy(geminiReq, "tools.0.functionDeclarations").Array()
		if len(decls) != 1 {
			t.Fatalf("functionDeclarations count = %d, want 1; req=%s", len(decls), geminiReq)
		}
		sentName := decls[0].Get("name").String()
		if sentName != "mcp_search" {
			t.Fatalf("sent declaration name = %q, want %q", sentName, "mcp_search")
		}

		roundTripUpstream := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"` + sentName + `","args":{"q":"test"}}}]},"finishReason":"STOP"}]}`)

		var param any
		streamOut := ConvertGeminiResponseToOpenAI(ctx, "gemini-4-argon", filteredReq, geminiReq, roundTripUpstream, &param)
		if len(streamOut) != 1 {
			t.Fatalf("stream output count = %d, want 1", len(streamOut))
		}
		if got := ojson.GetBytesNoCopy(streamOut[0], "choices.0.delta.tool_calls.0.function.name").String(); got != "mcp/search" {
			t.Fatalf("stream function.name = %q, want %q; payload=%s", got, "mcp/search", streamOut[0])
		}

		nonStreamOut := ConvertGeminiResponseToOpenAINonStream(ctx, "gemini-4-argon", filteredReq, geminiReq, roundTripUpstream, nil)
		if got := ojson.GetBytesNoCopy(nonStreamOut, "choices.0.message.tool_calls.0.function.name").String(); got != "mcp/search" {
			t.Fatalf("non-stream function.name = %q, want %q; payload=%s", got, "mcp/search", nonStreamOut)
		}
	})
}

func TestConvertGeminiResponseToOpenAI_SeparatesDeltaPayloadFromTerminalFinishReason(t *testing.T) {
	ctx := context.Background()

	assertNoChunkMixesDeltaAndFinishReason := func(t *testing.T, chunks [][]byte) {
		t.Helper()
		for i, chunk := range chunks {
			fr := ojson.GetBytesNoCopy(chunk, "choices.0.finish_reason")
			if !fr.Exists() || fr.Type == ojson.Null || fr.String() == "" {
				continue
			}
			delta := ojson.GetBytesNoCopy(chunk, "choices.0.delta")
			if content := delta.Get("content"); content.Exists() && content.Type != ojson.Null {
				t.Fatalf("chunk %d mixes finish_reason=%q with delta.content=%s: %s", i, fr.String(), content.Raw, chunk)
			}
			if reasoning := delta.Get("reasoning_content"); reasoning.Exists() && reasoning.Type != ojson.Null {
				t.Fatalf("chunk %d mixes finish_reason=%q with delta.reasoning_content=%s: %s", i, fr.String(), reasoning.Raw, chunk)
			}
			if toolCalls := delta.Get("tool_calls"); toolCalls.Exists() && toolCalls.Type != ojson.Null {
				t.Fatalf("chunk %d mixes finish_reason=%q with delta.tool_calls=%s: %s", i, fr.String(), toolCalls.Raw, chunk)
			}
			if images := delta.Get("images"); images.Exists() && images.Type != ojson.Null {
				t.Fatalf("chunk %d mixes finish_reason=%q with delta.images=%s: %s", i, fr.String(), images.Raw, chunk)
			}
		}
	}

	t.Run("parallel tool calls in terminal frame", func(t *testing.T) {
		var param any
		chunk1 := []byte(`{"responseId":"resp_gem_8039","modelVersion":"gemini-4-neon","createTime":"2026-11-23T00:00:00Z","candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"id":"call_a","name":"read","args":{"path":"A"}}}]}}]}`)
		chunk2 := []byte(`{"responseId":"resp_gem_8039","modelVersion":"gemini-4-neon","createTime":"2026-11-23T00:00:00Z","candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"id":"call_b","name":"read","args":{"path":"B"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":6,"totalTokenCount":16}}`)

		out1 := ConvertGeminiResponseToOpenAI(ctx, "gemini-4-neon", nil, nil, chunk1, &param)
		out2 := ConvertGeminiResponseToOpenAI(ctx, "gemini-4-neon", nil, nil, chunk2, &param)
		done := ConvertGeminiResponseToOpenAI(ctx, "gemini-4-neon", nil, nil, []byte("[DONE]"), &param)
		if len(done) != 0 {
			t.Fatalf("expected 0 chunks on [DONE] after terminal frame, got %d: %q", len(done), done)
		}
		if len(out2) != 2 {
			t.Fatalf("expected terminal tool-call frame to split into 2 chunks, got %d: %q", len(out2), out2)
		}

		all := append(append([][]byte(nil), out1...), out2...)
		assertNoChunkMixesDeltaAndFinishReason(t, all)

		if fr := ojson.GetBytesNoCopy(out2[0], "choices.0.finish_reason"); fr.Exists() && fr.Type != ojson.Null {
			t.Fatalf("payload chunk finish_reason = %s, want null; payload=%s", fr.Raw, out2[0])
		}
		if usage := ojson.GetBytesNoCopy(out2[0], "usage"); usage.Exists() {
			t.Fatalf("payload chunk must not carry usage: %s", out2[0])
		}
		if got := ojson.GetBytesNoCopy(out2[0], "choices.0.delta.tool_calls.0.index").Int(); got != 1 {
			t.Fatalf("second tool call index = %d, want 1; payload=%s", got, out2[0])
		}
		if got := ojson.GetBytesNoCopy(out2[0], "choices.0.delta.tool_calls.0.function.arguments").String(); got != `{"path":"B"}` {
			t.Fatalf("second tool call arguments = %q, want %q; payload=%s", got, `{"path":"B"}`, out2[0])
		}

		if got := ojson.GetBytesNoCopy(out2[1], "choices.0.delta").Raw; got != "{}" {
			t.Fatalf("terminal chunk delta = %s, want {}; payload=%s", got, out2[1])
		}
		if got := ojson.GetBytesNoCopy(out2[1], "choices.0.finish_reason").String(); got != "tool_calls" {
			t.Fatalf("terminal finish_reason = %q, want tool_calls; payload=%s", got, out2[1])
		}
		if got := ojson.GetBytesNoCopy(out2[1], "choices.0.native_finish_reason").String(); got != "stop" {
			t.Fatalf("terminal native_finish_reason = %q, want stop; payload=%s", got, out2[1])
		}
		if got := ojson.GetBytesNoCopy(out2[1], "usage.total_tokens").Int(); got != 16 {
			t.Fatalf("terminal usage.total_tokens = %d, want 16; payload=%s", got, out2[1])
		}
	})

	t.Run("multiple candidates split independently", func(t *testing.T) {
		var param any
		chunk := []byte(`{"responseId":"resp_multi_cand","modelVersion":"gemini-4-neon","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"cand0"}]},"finishReason":"STOP"},{"index":1,"content":{"role":"model","parts":[{"text":"cand1"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":8,"totalTokenCount":18}}`)

		out := ConvertGeminiResponseToOpenAI(ctx, "gemini-4-neon", nil, nil, chunk, &param)
		if len(out) != 4 {
			t.Fatalf("expected 4 chunks (payload+terminal per candidate), got %d: %q", len(out), out)
		}
		assertNoChunkMixesDeltaAndFinishReason(t, out)

		if got := ojson.GetBytesNoCopy(out[0], "choices.0.index").Int(); got != 0 {
			t.Fatalf("out[0] choice index = %d, want 0", got)
		}
		if got := ojson.GetBytesNoCopy(out[0], "choices.0.delta.content").String(); got != "cand0" {
			t.Fatalf("out[0] delta.content = %q, want cand0", got)
		}
		if usage := ojson.GetBytesNoCopy(out[0], "usage"); usage.Exists() {
			t.Fatalf("out[0] payload chunk must not carry usage: %s", out[0])
		}
		if got := ojson.GetBytesNoCopy(out[1], "choices.0.index").Int(); got != 0 {
			t.Fatalf("out[1] choice index = %d, want 0", got)
		}
		if got := ojson.GetBytesNoCopy(out[1], "choices.0.delta").Raw; got != "{}" {
			t.Fatalf("out[1] terminal delta = %s, want {}", got)
		}
		if got := ojson.GetBytesNoCopy(out[1], "choices.0.finish_reason").String(); got != "stop" {
			t.Fatalf("out[1] finish_reason = %q, want stop", got)
		}
		if got := ojson.GetBytesNoCopy(out[1], "usage.total_tokens").Int(); got != 18 {
			t.Fatalf("out[1] usage.total_tokens = %d, want 18", got)
		}

		if got := ojson.GetBytesNoCopy(out[2], "choices.0.index").Int(); got != 1 {
			t.Fatalf("out[2] choice index = %d, want 1", got)
		}
		if got := ojson.GetBytesNoCopy(out[2], "choices.0.delta.content").String(); got != "cand1" {
			t.Fatalf("out[2] delta.content = %q, want cand1", got)
		}
		if usage := ojson.GetBytesNoCopy(out[2], "usage"); usage.Exists() {
			t.Fatalf("out[2] payload chunk must not carry usage: %s", out[2])
		}
		if got := ojson.GetBytesNoCopy(out[3], "choices.0.index").Int(); got != 1 {
			t.Fatalf("out[3] choice index = %d, want 1", got)
		}
		if got := ojson.GetBytesNoCopy(out[3], "choices.0.delta").Raw; got != "{}" {
			t.Fatalf("out[3] terminal delta = %s, want {}", got)
		}
		if got := ojson.GetBytesNoCopy(out[3], "choices.0.finish_reason").String(); got != "length" {
			t.Fatalf("out[3] finish_reason = %q, want length", got)
		}
		if got := ojson.GetBytesNoCopy(out[3], "usage.total_tokens").Int(); got != 18 {
			t.Fatalf("out[3] usage.total_tokens = %d, want 18", got)
		}
	})

	t.Run("empty text part on terminal frame stays single clean terminal chunk", func(t *testing.T) {
		var param any
		chunk1 := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"read","args":{"path":"A"}}}]}}]}`)
		chunk2 := []byte(`{"candidates":[{"content":{"parts":[{"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":1,"totalTokenCount":6}}`)

		_ = ConvertGeminiResponseToOpenAI(ctx, "gemini-4-neon", nil, nil, chunk1, &param)
		out2 := ConvertGeminiResponseToOpenAI(ctx, "gemini-4-neon", nil, nil, chunk2, &param)
		if len(out2) != 1 {
			t.Fatalf("expected empty-text terminal frame to emit 1 chunk, got %d: %q", len(out2), out2)
		}
		assertNoChunkMixesDeltaAndFinishReason(t, out2)
		if got := ojson.GetBytesNoCopy(out2[0], "choices.0.finish_reason").String(); got != "tool_calls" {
			t.Fatalf("terminal finish_reason = %q, want tool_calls; payload=%s", got, out2[0])
		}
		if got := ojson.GetBytesNoCopy(out2[0], "choices.0.delta").Raw; got != "{}" {
			t.Fatalf("terminal delta = %s, want {}; payload=%s", got, out2[0])
		}
		if got := ojson.GetBytesNoCopy(out2[0], "usage.total_tokens").Int(); got != 6 {
			t.Fatalf("terminal usage.total_tokens = %d, want 6; payload=%s", got, out2[0])
		}
	})

	t.Run("separate non-payload terminal frame uses empty delta", func(t *testing.T) {
		var param any
		chunk1 := []byte(`{"responseId":"resp_gem_sep","modelVersion":"gemini-4-neon","candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"id":"call_a","name":"read","args":{"path":"A"}}}]}}]}`)
		chunk2 := []byte(`{"candidates":[{"index":0,"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":4,"totalTokenCount":14}}`)

		_ = ConvertGeminiResponseToOpenAI(ctx, "gemini-4-neon", nil, nil, chunk1, &param)
		out2 := ConvertGeminiResponseToOpenAI(ctx, "gemini-4-neon", nil, nil, chunk2, &param)
		if len(out2) != 1 {
			t.Fatalf("expected 1 terminal chunk, got %d: %q", len(out2), out2)
		}
		if got := ojson.GetBytesNoCopy(out2[0], "choices.0.delta").Raw; got != "{}" {
			t.Fatalf("terminal delta = %s, want {}; payload=%s", got, out2[0])
		}
		if got := ojson.GetBytesNoCopy(out2[0], "choices.0.finish_reason").String(); got != "tool_calls" {
			t.Fatalf("terminal finish_reason = %q, want tool_calls; payload=%s", got, out2[0])
		}
	})

	t.Run("DONE and standalone trailing usage terminal chunks use empty delta", func(t *testing.T) {
		// 1. [DONE] synthesis path
		var doneParam any
		doneChunk1 := []byte(`{"responseId":"resp_done","modelVersion":"gemini-4-neon","candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"id":"call_a","name":"read","args":{"path":"A"}}}]},"finishReason":"STOP"}]}`)
		_ = ConvertGeminiResponseToOpenAI(ctx, "gemini-4-neon", nil, nil, doneChunk1, &doneParam)
		doneOut := ConvertGeminiResponseToOpenAI(ctx, "gemini-4-neon", nil, nil, []byte("[DONE]"), &doneParam)
		if len(doneOut) != 1 {
			t.Fatalf("expected 1 DONE terminal chunk, got %d: %q", len(doneOut), doneOut)
		}
		if got := ojson.GetBytesNoCopy(doneOut[0], "choices.0.delta").Raw; got != "{}" {
			t.Fatalf("DONE terminal delta = %s, want {}; payload=%s", got, doneOut[0])
		}
		if got := ojson.GetBytesNoCopy(doneOut[0], "choices.0.finish_reason").String(); got != "tool_calls" {
			t.Fatalf("DONE terminal finish_reason = %q, want tool_calls; payload=%s", got, doneOut[0])
		}

		// 2. Standalone trailing usage finalizing cached finishReason
		var usageParam any
		usageChunk1 := []byte(`{"responseId":"resp_usage","modelVersion":"gemini-4-neon","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}]}`)
		usageChunk2 := []byte(`{"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2,"totalTokenCount":7}}`)
		_ = ConvertGeminiResponseToOpenAI(ctx, "gemini-4-neon", nil, nil, usageChunk1, &usageParam)
		usageOut := ConvertGeminiResponseToOpenAI(ctx, "gemini-4-neon", nil, nil, usageChunk2, &usageParam)
		if len(usageOut) != 1 {
			t.Fatalf("expected 1 trailing usage terminal chunk, got %d: %q", len(usageOut), usageOut)
		}
		if got := ojson.GetBytesNoCopy(usageOut[0], "choices.0.delta").Raw; got != "{}" {
			t.Fatalf("trailing usage terminal delta = %s, want {}; payload=%s", got, usageOut[0])
		}
		if got := ojson.GetBytesNoCopy(usageOut[0], "choices.0.finish_reason").String(); got != "stop" {
			t.Fatalf("trailing usage terminal finish_reason = %q, want stop; payload=%s", got, usageOut[0])
		}
	})

	t.Run("duplicate terminal frame deduplicates per choice without blocking other choices", func(t *testing.T) {
		var param any
		chunk1 := []byte(`{"responseId":"resp_dup_gem","modelVersion":"gemini-4-neon","candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"id":"call_a","name":"read","args":{"path":"A"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":4,"totalTokenCount":14}}`)
		chunk2 := []byte(`{"candidates":[{"index":0,"finishReason":"STOP"},{"index":1,"content":{"role":"model","parts":[{"text":"other"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":6,"totalTokenCount":16}}`)

		out1 := ConvertGeminiResponseToOpenAI(ctx, "gemini-4-neon", nil, nil, chunk1, &param)
		out2 := ConvertGeminiResponseToOpenAI(ctx, "gemini-4-neon", nil, nil, chunk2, &param)
		if len(out1) != 2 {
			t.Fatalf("expected 2 chunks from chunk1, got %d: %q", len(out1), out1)
		}
		if len(out2) != 2 {
			t.Fatalf("expected 2 chunks from chunk2 (only choice 1 payload+terminal), got %d: %q", len(out2), out2)
		}
		assertNoChunkMixesDeltaAndFinishReason(t, append(append([][]byte(nil), out1...), out2...))
		if got := ojson.GetBytesNoCopy(out2[0], "choices.0.index").Int(); got != 1 {
			t.Fatalf("out2[0] choice index = %d, want 1; payload=%s", got, out2[0])
		}
		if got := ojson.GetBytesNoCopy(out2[0], "choices.0.delta.content").String(); got != "other" {
			t.Fatalf("out2[0] delta.content = %q, want other; payload=%s", got, out2[0])
		}
		if got := ojson.GetBytesNoCopy(out2[1], "choices.0.index").Int(); got != 1 {
			t.Fatalf("out2[1] choice index = %d, want 1; payload=%s", got, out2[1])
		}
		if got := ojson.GetBytesNoCopy(out2[1], "choices.0.delta").Raw; got != "{}" {
			t.Fatalf("out2[1] terminal delta = %s, want {}; payload=%s", got, out2[1])
		}
		if got := ojson.GetBytesNoCopy(out2[1], "choices.0.finish_reason").String(); got != "stop" {
			t.Fatalf("out2[1] finish_reason = %q, want stop; payload=%s", got, out2[1])
		}
	})
}

func TestConvertGeminiResponseToOpenAI_NullOrEmptyFinishReasonDoesNotClobberCachedReason(t *testing.T) {
	ctx := context.Background()

	t.Run("null finishReason in trailing usage chunk preserves cached finishReason", func(t *testing.T) {
		var param any
		chunk1 := []byte(`{"candidates":[{"index":0,"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}]}`)
		chunk2 := []byte(`{"candidates":[{"index":0,"finishReason":null}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2,"totalTokenCount":7}}`)

		_ = ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-flash", nil, nil, chunk1, &param)
		out2 := ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-flash", nil, nil, chunk2, &param)
		if len(out2) != 1 {
			t.Fatalf("output count = %d, want 1; outputs=%q", len(out2), out2)
		}
		if got := ojson.GetBytesNoCopy(out2[0], "choices.0.finish_reason").String(); got != "stop" {
			t.Fatalf("finish_reason = %q, want %q; payload=%s", got, "stop", out2[0])
		}
		if got := ojson.GetBytesNoCopy(out2[0], "choices.0.native_finish_reason").String(); got != "stop" {
			t.Fatalf("native_finish_reason = %q, want %q; payload=%s", got, "stop", out2[0])
		}
	})

	t.Run("empty or whitespace finishReason preserves cached finishReason until DONE", func(t *testing.T) {
		var param any
		chunk1 := []byte(`{"candidates":[{"index":0,"content":{"parts":[{"text":"hi"}]},"finishReason":"MAX_TOKENS"}]}`)
		chunk2 := []byte(`{"candidates":[{"index":0,"finishReason":""}]}`)
		chunk3 := []byte(`{"candidates":[{"index":0,"finishReason":"   "}]}`)

		_ = ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-flash", nil, nil, chunk1, &param)
		_ = ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-flash", nil, nil, chunk2, &param)
		_ = ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-flash", nil, nil, chunk3, &param)
		done := ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-flash", nil, nil, []byte("[DONE]"), &param)
		if len(done) != 1 {
			t.Fatalf("DONE output count = %d, want 1; outputs=%q", len(done), done)
		}
		if got := ojson.GetBytesNoCopy(done[0], "choices.0.finish_reason").String(); got != "length" {
			t.Fatalf("finish_reason = %q, want %q; payload=%s", got, "length", done[0])
		}
		if got := ojson.GetBytesNoCopy(done[0], "choices.0.native_finish_reason").String(); got != "max_tokens" {
			t.Fatalf("native_finish_reason = %q, want %q; payload=%s", got, "max_tokens", done[0])
		}
	})

	t.Run("non-streaming null or empty finishReason keeps null finish_reason", func(t *testing.T) {
		for _, rawFinish := range []string{"null", `""`, `"   "`} {
			upstream := []byte(`{"candidates":[{"index":0,"content":{"parts":[{"text":"hi"}]},"finishReason":` + rawFinish + `}]}`)
			output := ConvertGeminiResponseToOpenAINonStream(ctx, "gemini-2.5-flash", nil, nil, upstream, nil)
			fr := ojson.GetBytesNoCopy(output, "choices.0.finish_reason")
			if fr.Type != ojson.Null {
				t.Fatalf("finishReason=%s: finish_reason = %s, want null; payload=%s", rawFinish, fr.Raw, output)
			}
			nfr := ojson.GetBytesNoCopy(output, "choices.0.native_finish_reason")
			if nfr.Type != ojson.Null {
				t.Fatalf("finishReason=%s: native_finish_reason = %s, want null; payload=%s", rawFinish, nfr.Raw, output)
			}
		}
	})
}

func TestConvertGeminiResponseToOpenAI_FunctionCallNullOrNonObjectArgsUsesEmptyObject(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		rawArgs string
	}{
		{name: "null", rawArgs: "null"},
		{name: "array", rawArgs: "[]"},
		{name: "string", rawArgs: `"invalid"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"get_time","args":` + tc.rawArgs + `}}]},"finishReason":"STOP"}]}`)

			var param any
			streamOut := ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-flash", nil, nil, upstream, &param)
			if len(streamOut) != 1 {
				t.Fatalf("streaming output count = %d, want 1", len(streamOut))
			}
			streamArgs := ojson.GetBytesNoCopy(streamOut[0], "choices.0.delta.tool_calls.0.function.arguments")
			if streamArgs.Type != ojson.String || streamArgs.String() != "{}" {
				t.Fatalf("streaming function.arguments = %s, want JSON string %q; payload=%s", streamArgs.Raw, "{}", streamOut[0])
			}

			nonStreamOut := ConvertGeminiResponseToOpenAINonStream(ctx, "gemini-2.5-flash", nil, nil, upstream, nil)
			nonStreamArgs := ojson.GetBytesNoCopy(nonStreamOut, "choices.0.message.tool_calls.0.function.arguments")
			if nonStreamArgs.Type != ojson.String || nonStreamArgs.String() != "{}" {
				t.Fatalf("non-streaming function.arguments = %s, want JSON string %q; payload=%s", nonStreamArgs.Raw, "{}", nonStreamOut)
			}
		})
	}

	t.Run("streaming nameless functionCall with null args is treated as noop", func(t *testing.T) {
		var param any
		upstream := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"","args":null}}]}}]}`)
		out := ConvertGeminiResponseToOpenAI(ctx, "gemini-2.5-flash", nil, nil, upstream, &param)
		if len(out) != 0 {
			t.Fatalf("expected 0 chunks for nameless functionCall with null args, got %d: %q", len(out), out)
		}
	})
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
