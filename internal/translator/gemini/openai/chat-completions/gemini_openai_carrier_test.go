package chat_completions

import (
	"context"
	"strconv"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestGeminiOpenAIPartMetadataPreservesSupportedPayload(t *testing.T) {
	for _, tt := range []struct {
		name string
		part string
		tool bool
	}{
		{"text", `{"text":"answer","partMetadata":{"source":"model"}}`, false},
		{"tool", `{"functionCall":{"name":"lookup","args":{}},"partMetadata":{"source":"model"}}`, true},
	} {
		for _, stream := range []bool{false, true} {
			for _, signed := range []bool{false, true} {
				t.Run(tt.name+"/stream="+strconv.FormatBool(stream)+"/signed="+strconv.FormatBool(signed), func(t *testing.T) {
					part := ojson.NewDocString(tt.part)
					if signed {
						_ = part.Set("thoughtSignature", capturedGeminiToolCallThoughtSignature)
					}
					upstream := []byte(`{"candidates":[{"content":{"parts":[` + part.String() + `]},"finishReason":"STOP"}]}`)
					var message ojson.Result
					if stream {
						var param any
						chunks := ConvertGeminiResponseToOpenAI(context.Background(), "gemini-4-carbon", nil, nil, upstream, &param)
						if len(chunks) != 1 {
							t.Fatalf("supported metadata part produced %d chunks", len(chunks))
						}
						message = ojson.GetBytes(chunks[0], "choices.0.delta")
					} else {
						response := ConvertGeminiResponseToOpenAINonStream(context.Background(), "gemini-4-carbon", nil, nil, upstream, nil)
						message = ojson.GetBytes(response, "choices.0.message")
					}
					if tt.tool {
						call := message.Get("tool_calls.0")
						if call.Get("function.name").String() != "lookup" || call.Get("function.arguments").String() != `{}` {
							t.Errorf("metadata dropped tool payload: %s", message.Raw)
						}
						if signed && call.Get("extra_content.google.thought_signature").String() != capturedGeminiToolCallThoughtSignature {
							t.Errorf("metadata dropped tool signature: %s", message.Raw)
						}
					} else if message.Get("content").String() != "answer" {
						t.Errorf("metadata dropped text payload: %s", message.Raw)
					}
				})
			}
		}
	}
}

func TestGeminiOpenAIUnsupportedPayloadDoesNotRetargetSignature(t *testing.T) {
	for _, tt := range []struct {
		name  string
		parts string
	}{
		{"file data", `{"fileData":{"mimeType":"image/png","fileUri":"gs://bucket/image"},"thoughtSignature":"` + capturedGeminiToolCallThoughtSignature + `"}`},
		{"code", `{"executableCode":{"language":"PYTHON","code":"1"},"thoughtSignature":"` + capturedGeminiToolCallThoughtSignature + `"}`},
		{"delayed file signature", `{"fileData":{"mimeType":"image/png","fileUri":"gs://bucket/image"}},{"thoughtSignature":"` + capturedGeminiToolCallThoughtSignature + `"}`},
		{"delayed code signature", `{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"1"}},{"thoughtSignature":"` + capturedGeminiToolCallThoughtSignature + `"}`},
	} {
		for _, stream := range []bool{false, true} {
			name := tt.name + "/nonstream"
			if stream {
				name = tt.name + "/stream"
			}
			t.Run(name, func(t *testing.T) {
				upstream := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{}}},` + tt.parts + `]},"finishReason":"STOP"}]}`)
				var calls []ojson.Result
				if stream {
					var param any
					for _, chunk := range ConvertGeminiResponseToOpenAI(context.Background(), "gemini-4-carbon", nil, nil, upstream, &param) {
						calls = append(calls, ojson.GetBytes(chunk, "choices.0.delta.tool_calls").Array()...)
					}
				} else {
					response := ConvertGeminiResponseToOpenAINonStream(context.Background(), "gemini-4-carbon", nil, nil, upstream, nil)
					calls = ojson.GetBytes(response, "choices.0.message.tool_calls").Array()
				}
				if len(calls) == 0 {
					t.Fatal("unrelated tool call was removed")
				}
				for _, call := range calls {
					if call.Get("extra_content.google.thought_signature").Exists() {
						t.Errorf("unsupported payload signature was attached to preceding tool: %s", call.Raw)
					}
				}
			})
		}
	}
}

func TestGeminiOpenAIStreamingLateToolSignature(t *testing.T) {
	for _, tt := range []struct {
		name string
		part string
	}{
		{"signature only", `{"thoughtSignature":"` + capturedGeminiToolCallThoughtSignature + `"}`},
		{"empty text signature", `{"text":"","thought_signature":"` + capturedGeminiToolCallThoughtSignature + `"}`},
		{"empty function signature", `{"functionCall":{},"thoughtSignature":"` + capturedGeminiToolCallThoughtSignature + `"}`},
		{"metadata only continuation", `{"functionCall":{"willContinue":true},"thoughtSignature":"` + capturedGeminiToolCallThoughtSignature + `"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var param any
			first := []byte(`{"candidates":[{"index":2,"content":{"parts":[{"functionCall":{"name":"lookup","partialArgs":[{"jsonPath":"$.q","stringValue":"Paris"}],"willContinue":true}}]}}]}`)
			initial := ConvertGeminiResponseToOpenAI(context.Background(), "gemini-4-carbon", nil, nil, first, &param)
			if len(initial) != 1 || ojson.GetBytes(initial[0], "choices.0.delta.tool_calls.0.index").Int() != 0 {
				t.Fatalf("missing initial tool call: %s", initial)
			}
			late := []byte(`{"candidates":[{"index":2,"content":{"parts":[` + tt.part + `]}}]}`)
			chunks := ConvertGeminiResponseToOpenAI(context.Background(), "gemini-4-carbon", nil, nil, late, &param)
			if len(chunks) != 1 {
				t.Fatalf("late signature must produce one delta, got %d", len(chunks))
			}
			call := ojson.GetBytes(chunks[0], "choices.0.delta.tool_calls.0")
			if ojson.GetBytes(chunks[0], "choices.0.index").Int() != 2 || !call.Get("index").Exists() || call.Get("index").Int() != 0 || call.Get("extra_content.google.thought_signature").String() != capturedGeminiToolCallThoughtSignature {
				t.Fatalf("late signature lost tool/candidate association: %s", chunks[0])
			}
			last := []byte(`{"candidates":[{"index":2,"content":{"parts":[{"functionCall":{}}]},"finishReason":"STOP"}]}`)
			ConvertGeminiResponseToOpenAI(context.Background(), "gemini-4-carbon", nil, nil, last, &param)
			done := ConvertGeminiResponseToOpenAI(context.Background(), "gemini-4-carbon", nil, nil, []byte("[DONE]"), &param)
			if len(done) != 1 || ojson.GetBytes(done[0], "choices.0.finish_reason").String() != "tool_calls" {
				t.Fatalf("late signature interrupted tool call completion: %s", done)
			}
		})
	}
}

func TestGeminiOpenAIVertexInitialToolSignature(t *testing.T) {
	upstream := []byte(`{"candidates":[{"index":1,"content":{"parts":[{"functionCall":{"name":"lookup","partialArgs":[{"jsonPath":"$.q","stringValue":"Paris"}],"willContinue":false},"thought_signature":"` + capturedGeminiToolCallThoughtSignature + `"}]}}]}`)
	var param any
	chunks := ConvertGeminiResponseToOpenAI(context.Background(), "gemini-4-carbon", nil, nil, upstream, &param)
	if len(chunks) != 1 {
		t.Fatalf("expected initial tool delta, got %s", chunks)
	}
	call := ojson.GetBytes(chunks[0], "choices.0.delta.tool_calls.0")
	if call.Get("extra_content.google.thought_signature").String() != capturedGeminiToolCallThoughtSignature || call.Get("function.arguments").String() != `{"q":"Paris"}` {
		t.Fatalf("signed Vertex call was not emitted intact: %s", chunks[0])
	}
}

func TestGeminiOpenAILateSignatureDoesNotCrossText(t *testing.T) {
	var param any
	chunks := []string{
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{}}}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"answer"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"thoughtSignature":"` + capturedGeminiToolCallThoughtSignature + `"}]}}]}`,
	}
	for _, raw := range chunks {
		for _, output := range ConvertGeminiResponseToOpenAI(context.Background(), "gemini-4-carbon", nil, nil, []byte(raw), &param) {
			if ojson.GetBytes(output, "choices.0.delta.tool_calls.0.extra_content").Exists() {
				t.Fatalf("text signature was attached to an earlier tool: %s", output)
			}
		}
	}
}

func TestGeminiOpenAINonStreamTrailingToolSignatureRoundTrip(t *testing.T) {
	for _, tt := range []struct {
		name    string
		between string
		want    string
	}{
		{"adjacent", "", capturedGeminiToolCallThoughtSignature},
		{"empty text", `{"text":""},`, capturedGeminiToolCallThoughtSignature},
		{"text boundary", `{"text":"answer"},`, ""},
		{"image boundary", `{"inlineData":{"mimeType":"image/png","data":"eA=="}},`, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			upstream := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{}}},` + tt.between + `{"thoughtSignature":"` + capturedGeminiToolCallThoughtSignature + `"}]},"finishReason":"STOP"}]}`)
			response := ConvertGeminiResponseToOpenAINonStream(context.Background(), "gemini-4-carbon", nil, nil, upstream, nil)
			call := ojson.GetBytes(response, "choices.0.message.tool_calls.0")
			if got := call.Get("extra_content.google.thought_signature").String(); got != tt.want {
				t.Fatalf("tail signature = %q, want %q; response=%s", got, tt.want, response)
			}
			request := ojson.NewDocString(`{"messages":[{"role":"assistant","tool_calls":[]},{"role":"tool","tool_call_id":"","content":"ok"}]}`)
			_ = request.SetRaw("messages.0.tool_calls.0", call.Raw)
			_ = request.Set("messages.1.tool_call_id", call.Get("id").String())
			output, errConvert := ConvertOpenAIRequestToGemini("gemini-4-carbon", request.Bytes(), false)
			if errConvert != nil {
				t.Fatal(errConvert)
			}
			want := tt.want
			if want == "" {
				want = "skip_thought_signature_validator"
			}
			if got := ojson.GetBytes(output, "contents.0.parts.0.thoughtSignature").String(); got != want {
				t.Fatalf("replayed tail signature = %q, want %q; output=%s", got, want, output)
			}
		})
	}
}

func TestGeminiOpenAIToolSignatureDoesNotExposeBypass(t *testing.T) {
	for _, signature := range []string{"", "skip_thought_signature_validator", "gemini#skip_thought_signature_validator"} {
		upstream := ojson.NewDocString(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{}}}]}}]}`)
		_ = upstream.Set("candidates.0.content.parts.0.thoughtSignature", signature)
		response := ConvertGeminiResponseToOpenAINonStream(context.Background(), "gemini-4-carbon", nil, nil, upstream.Bytes(), nil)
		if ojson.GetBytes(response, "choices.0.message.tool_calls.0.extra_content").Exists() {
			t.Fatalf("bypass must not be exposed as a real signature: %s", response)
		}
		var param any
		for _, chunk := range ConvertGeminiResponseToOpenAI(context.Background(), "gemini-4-carbon", nil, nil, upstream.Bytes(), &param) {
			if ojson.GetBytes(chunk, "choices.0.delta.tool_calls.0.extra_content").Exists() {
				t.Fatalf("stream exposed bypass signature: %s", chunk)
			}
		}
	}
}

func TestGeminiOpenAIToolThoughtSignatureRoundTrip(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "nonstream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			upstream := ojson.NewDocString(`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"q":"Paris"}}}]},"finishReason":"STOP"}]}`)
			_ = upstream.Set("candidates.0.content.parts.0.thoughtSignature", capturedGeminiToolCallThoughtSignature)
			var toolCall ojson.Result
			if stream {
				var param any
				chunks := ConvertGeminiResponseToOpenAI(context.Background(), "gemini-4-carbon", nil, nil, upstream.Bytes(), &param)
				if len(chunks) != 1 {
					t.Fatalf("expected one response chunk, got %d", len(chunks))
				}
				toolCall = ojson.GetBytes(chunks[0], "choices.0.delta.tool_calls.0")
			} else {
				response := ConvertGeminiResponseToOpenAINonStream(context.Background(), "gemini-4-carbon", nil, nil, upstream.Bytes(), nil)
				toolCall = ojson.GetBytes(response, "choices.0.message.tool_calls.0")
			}
			if got := toolCall.Get("extra_content.google.thought_signature").String(); got != capturedGeminiToolCallThoughtSignature {
				t.Fatalf("tool call signature = %q, want %q; call=%s", got, capturedGeminiToolCallThoughtSignature, toolCall.Raw)
			}
			request := ojson.NewDocString(`{"messages":[{"role":"assistant","tool_calls":[]},{"role":"tool","tool_call_id":"","content":"ok"}]}`)
			_ = request.SetRaw("messages.0.tool_calls.0", toolCall.Raw)
			_ = request.Set("messages.1.tool_call_id", toolCall.Get("id").String())
			output, errConvert := ConvertOpenAIRequestToGemini("gemini-4-carbon", request.Bytes(), false)
			if errConvert != nil {
				t.Fatal(errConvert)
			}
			if got := ojson.GetBytes(output, "contents.0.parts.0.thoughtSignature").String(); got != capturedGeminiToolCallThoughtSignature {
				t.Fatalf("replayed signature = %q, want original signature; output=%s", got, output)
			}
		})
	}
}
