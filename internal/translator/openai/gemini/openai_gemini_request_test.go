package gemini

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiRequestToOpenAI_FunctionResponsesConsumeToolCallIDsFIFO(t *testing.T) {
	inputJSON := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "read_file", "args": {"path": "a.txt"}}},
					{"functionCall": {"name": "grep", "args": {"pattern": "needle"}}},
					{"functionCall": {"name": "list_dir", "args": {"path": "."}}}
				]
			},
			{
				"role": "function",
				"parts": [
					{"functionResponse": {"name": "read_file", "response": {"result": "a"}}},
					{"functionResponse": {"name": "grep", "response": {"result": "b"}}},
					{"functionResponse": {"name": "list_dir", "response": {"result": "c"}}}
				]
			}
		]
	}`)

	out := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	firstID := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String()
	secondID := gjson.GetBytes(out, "messages.0.tool_calls.1.id").String()
	thirdID := gjson.GetBytes(out, "messages.0.tool_calls.2.id").String()

	if firstID == "" || secondID == "" || thirdID == "" {
		t.Fatalf("expected all assistant tool call IDs to be set. Output: %s", string(out))
	}
	if firstID == secondID || secondID == thirdID || firstID == thirdID {
		t.Fatalf("expected distinct assistant tool call IDs, got %q, %q, %q", firstID, secondID, thirdID)
	}
	if got := gjson.GetBytes(out, "messages.1.tool_call_id").String(); got != firstID {
		t.Fatalf("messages.1.tool_call_id = %q, want %q. Output: %s", got, firstID, string(out))
	}
	if got := gjson.GetBytes(out, "messages.2.tool_call_id").String(); got != secondID {
		t.Fatalf("messages.2.tool_call_id = %q, want %q. Output: %s", got, secondID, string(out))
	}
	if got := gjson.GetBytes(out, "messages.3.tool_call_id").String(); got != thirdID {
		t.Fatalf("messages.3.tool_call_id = %q, want %q. Output: %s", got, thirdID, string(out))
	}
}

func TestConvertGeminiRequestToOpenAI_FunctionResponseWithoutPriorCallGetsFallbackID(t *testing.T) {
	inputJSON := []byte(`{
		"contents": [
			{
				"role": "function",
				"parts": [
					{"functionResponse": {"name": "read_file", "response": {"result": "ok"}}}
				]
			}
		]
	}`)

	out := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	toolCallID := gjson.GetBytes(out, "messages.0.tool_call_id").String()
	if !strings.HasPrefix(toolCallID, "call_") {
		t.Fatalf("fallback tool_call_id = %q, want call_ prefix. Output: %s", toolCallID, string(out))
	}
}

func TestConvertGeminiRequestToOpenAI_ExtraFunctionResponsesUseFallbackID(t *testing.T) {
	inputJSON := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "read_file", "args": {"path": "a.txt"}}}
				]
			},
			{
				"role": "function",
				"parts": [
					{"functionResponse": {"name": "read_file", "response": {"result": "a"}}},
					{"functionResponse": {"name": "read_file", "response": {"result": "extra"}}}
				]
			}
		]
	}`)

	out := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	callID := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String()
	firstResponseID := gjson.GetBytes(out, "messages.1.tool_call_id").String()
	extraResponseID := gjson.GetBytes(out, "messages.2.tool_call_id").String()

	if firstResponseID != callID {
		t.Fatalf("messages.1.tool_call_id = %q, want %q. Output: %s", firstResponseID, callID, string(out))
	}
	if !strings.HasPrefix(extraResponseID, "call_") {
		t.Fatalf("extra response fallback tool_call_id = %q, want call_ prefix. Output: %s", extraResponseID, string(out))
	}
	if extraResponseID == callID {
		t.Fatalf("extra response reused consumed tool_call_id %q. Output: %s", extraResponseID, string(out))
	}
}

func TestConvertGeminiRequestToOpenAI_PreservesExplicitFunctionCallIDs(t *testing.T) {
	tests := []struct {
		name          string
		callField     string
		responseField string
		want          string
	}{
		{
			name:          "id",
			callField:     `"id":"call_gateway_id"`,
			responseField: `"id":"call_gateway_id"`,
			want:          "call_gateway_id",
		},
		{
			name:          "call_id",
			callField:     `"call_id":"call_gateway_call_id"`,
			responseField: `"call_id":"call_gateway_call_id"`,
			want:          "call_gateway_call_id",
		},
		{
			name:          "callId",
			callField:     `"callId":"call_gateway_camel_id"`,
			responseField: `"callId":"call_gateway_camel_id"`,
			want:          "call_gateway_camel_id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputJSON := []byte(`{
				"contents": [
					{"role": "model", "parts": [{"functionCall": {"name": "lookup", ` + tt.callField + `, "args": {"q": "x"}}}]},
					{"role": "function", "parts": [{"functionResponse": {"name": "lookup", ` + tt.responseField + `, "response": {"result": "ok"}}}]}
				]
			}`)

			out := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
			if got := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String(); got != tt.want {
				t.Fatalf("tool call id = %q, want %q. Output: %s", got, tt.want, string(out))
			}
			if got := gjson.GetBytes(out, "messages.1.tool_call_id").String(); got != tt.want {
				t.Fatalf("tool response id = %q, want %q. Output: %s", got, tt.want, string(out))
			}
		})
	}
}

func TestConvertGeminiRequestToOpenAI_AcceptsSnakeInlineData(t *testing.T) {
	out := ConvertGeminiRequestToOpenAI("gpt-test", []byte(`{"contents":[{"role":"user","parts":[{"inline_data":{"mime_type":"image/png","data":"aGVsbG8="}}]}]}`), false)
	if got := gjson.GetBytes(out, "messages.0.content.0.image_url.url").String(); got != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("image url = %q, want data:image/png;base64,aGVsbG8=. Output: %s", got, string(out))
	}
}

func TestConvertGeminiRequestToOpenAI_SplitsNonImageInlineDataByMIME(t *testing.T) {
	out := ConvertGeminiRequestToOpenAI("gpt-test", []byte(`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"audio/wav","data":"UklGRg=="}},{"inlineData":{"mimeType":"video/mp4","data":"AAAAIGZ0eXA="}},{"inlineData":{"mimeType":"application/pdf","data":"JVBERi0="}}]}]}`), false)

	if got := gjson.GetBytes(out, "messages.0.content.0.type").String(); got != "input_audio" {
		t.Fatalf("audio content type = %q, want input_audio. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.1.type").String(); got != "video_url" {
		t.Fatalf("video content type = %q, want video_url. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.2.type").String(); got != "file" {
		t.Fatalf("document content type = %q, want file. Output: %s", got, string(out))
	}
	if gjson.GetBytes(out, "messages.0.content.#(type==\"image_url\")").Exists() {
		t.Fatalf("non-image inlineData must not be converted to image_url. Output: %s", string(out))
	}
}

func TestConvertGeminiRequestToOpenAI_DropsHiddenThoughtParts(t *testing.T) {
	t.Run("thought-only turn", func(t *testing.T) {
		out := ConvertGeminiRequestToOpenAI("openai-test", []byte(`{
			"contents":[
				{"role":"model","parts":[{"thought":true,"text":"internal reasoning","thoughtSignature":"opaque-provider-state"}]},
				{"role":"user","parts":[{"text":"continue"}]}
			]
		}`), false)

		messages := gjson.GetBytes(out, "messages").Array()
		if len(messages) != 1 || messages[0].Get("role").String() != "user" || messages[0].Get("content").String() != "continue" {
			t.Fatalf("hidden thought turn was not dropped. Output: %s", string(out))
		}
	})

	t.Run("mixed turn", func(t *testing.T) {
		out := ConvertGeminiRequestToOpenAI("openai-test", []byte(`{
			"contents":[{"role":"model","parts":[
				{"thought":true,"text":"internal reasoning","thoughtSignature":"opaque-provider-state"},
				{"text":"visible answer"}
			]}]
		}`), false)

		messages := gjson.GetBytes(out, "messages").Array()
		if len(messages) != 1 || messages[0].Get("role").String() != "assistant" || messages[0].Get("content").String() != "visible answer" {
			t.Fatalf("hidden thought was not dropped independently of visible text. Output: %s", string(out))
		}
	})
}

func TestConvertGeminiRequestToOpenAI_DeterministicToolCallIDs(t *testing.T) {
	inputJSON := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "read_file", "args": {"path": "main.go"}}},
					{"functionCall": {"name": "grep", "args": {"pattern": "TODO"}}}
				]
			},
			{
				"role": "function",
				"parts": [
					{"functionResponse": {"name": "read_file", "response": {"result": "code"}}},
					{"functionResponse": {"name": "grep", "response": {"result": "matches"}}}
				]
			}
		]
	}`)

	firstOut := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	firstCall0 := gjson.GetBytes(firstOut, "messages.0.tool_calls.0.id").String()
	firstCall1 := gjson.GetBytes(firstOut, "messages.0.tool_calls.1.id").String()
	firstResp0 := gjson.GetBytes(firstOut, "messages.1.tool_call_id").String()
	firstResp1 := gjson.GetBytes(firstOut, "messages.2.tool_call_id").String()

	if !strings.HasPrefix(firstCall0, "call_") || !strings.HasPrefix(firstCall1, "call_") {
		t.Fatalf("expected tool call IDs to have call_ prefix, got %q, %q", firstCall0, firstCall1)
	}
	if firstResp0 != firstCall0 {
		t.Fatalf("expected first response ID %q to match first call ID %q", firstResp0, firstCall0)
	}
	if firstResp1 != firstCall1 {
		t.Fatalf("expected second response ID %q to match second call ID %q", firstResp1, firstCall1)
	}

	for i := 0; i < 100; i++ {
		out := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
		if got := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String(); got != firstCall0 {
			t.Fatalf("iteration %d: tool_calls.0.id = %q, want %q", i, got, firstCall0)
		}
		if got := gjson.GetBytes(out, "messages.0.tool_calls.1.id").String(); got != firstCall1 {
			t.Fatalf("iteration %d: tool_calls.1.id = %q, want %q", i, got, firstCall1)
		}
		if got := gjson.GetBytes(out, "messages.1.tool_call_id").String(); got != firstResp0 {
			t.Fatalf("iteration %d: messages.1.tool_call_id = %q, want %q", i, got, firstResp0)
		}
		if got := gjson.GetBytes(out, "messages.2.tool_call_id").String(); got != firstResp1 {
			t.Fatalf("iteration %d: messages.2.tool_call_id = %q, want %q", i, got, firstResp1)
		}
	}
}

func TestConvertGeminiRequestToOpenAI_SameNameCallsInSameMessageDistinct(t *testing.T) {
	inputJSON := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "read_file", "args": {"path": "a.txt"}}},
					{"functionCall": {"name": "read_file", "args": {"path": "a.txt"}}}
				]
			},
			{
				"role": "function",
				"parts": [
					{"functionResponse": {"name": "read_file", "response": {"result": "first"}}},
					{"functionResponse": {"name": "read_file", "response": {"result": "second"}}}
				]
			}
		]
	}`)

	out := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	id0 := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String()
	id1 := gjson.GetBytes(out, "messages.0.tool_calls.1.id").String()

	if id0 == id1 {
		t.Fatalf("expected distinct IDs for same-name calls in same message, got both %q", id0)
	}

	resp0 := gjson.GetBytes(out, "messages.1.tool_call_id").String()
	resp1 := gjson.GetBytes(out, "messages.2.tool_call_id").String()

	if resp0 != id0 {
		t.Fatalf("expected first response to match first call ID %q, got %q", id0, resp0)
	}
	if resp1 != id1 {
		t.Fatalf("expected second response to match second call ID %q, got %q", id1, resp1)
	}
}

func TestConvertGeminiRequestToOpenAI_InterleavedPerNameFIFOMatching(t *testing.T) {
	// Interleaved calls: toolA, toolB, toolA, toolB
	// Responses returned grouped by tool: toolB, toolA, toolB, toolA
	inputJSON := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "tool_a", "args": {"step": 1}}},
					{"functionCall": {"name": "tool_b", "args": {"step": 1}}},
					{"functionCall": {"name": "tool_a", "args": {"step": 2}}},
					{"functionCall": {"name": "tool_b", "args": {"step": 2}}}
				]
			},
			{
				"role": "function",
				"parts": [
					{"functionResponse": {"name": "tool_b", "response": {"step": 1}}},
					{"functionResponse": {"name": "tool_a", "response": {"step": 1}}},
					{"functionResponse": {"name": "tool_b", "response": {"step": 2}}},
					{"functionResponse": {"name": "tool_a", "response": {"step": 2}}}
				]
			}
		]
	}`)

	out := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	callA1 := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String()
	callB1 := gjson.GetBytes(out, "messages.0.tool_calls.1.id").String()
	callA2 := gjson.GetBytes(out, "messages.0.tool_calls.2.id").String()
	callB2 := gjson.GetBytes(out, "messages.0.tool_calls.3.id").String()

	// Responses:
	// messages[1] = tool_b (step 1) -> should match callB1
	// messages[2] = tool_a (step 1) -> should match callA1
	// messages[3] = tool_b (step 2) -> should match callB2
	// messages[4] = tool_a (step 2) -> should match callA2
	if got := gjson.GetBytes(out, "messages.1.tool_call_id").String(); got != callB1 {
		t.Fatalf("first response (tool_b) = %q, want callB1 %q", got, callB1)
	}
	if got := gjson.GetBytes(out, "messages.2.tool_call_id").String(); got != callA1 {
		t.Fatalf("second response (tool_a) = %q, want callA1 %q", got, callA1)
	}
	if got := gjson.GetBytes(out, "messages.3.tool_call_id").String(); got != callB2 {
		t.Fatalf("third response (tool_b) = %q, want callB2 %q", got, callB2)
	}
	if got := gjson.GetBytes(out, "messages.4.tool_call_id").String(); got != callA2 {
		t.Fatalf("fourth response (tool_a) = %q, want callA2 %q", got, callA2)
	}
}

func TestConvertGeminiRequestToOpenAI_DeterministicFallbackOrphanResponse(t *testing.T) {
	inputJSON := []byte(`{
		"contents": [
			{
				"role": "function",
				"parts": [
					{"functionResponse": {"name": "orphan_tool", "response": {"result": "standalone"}}}
				]
			}
		]
	}`)

	firstOut := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	firstID := gjson.GetBytes(firstOut, "messages.0.tool_call_id").String()
	if !strings.HasPrefix(firstID, "call_") {
		t.Fatalf("expected fallback tool_call_id with call_ prefix, got %q", firstID)
	}

	for i := 0; i < 100; i++ {
		out := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
		if got := gjson.GetBytes(out, "messages.0.tool_call_id").String(); got != firstID {
			t.Fatalf("iteration %d: orphan fallback tool_call_id = %q, want %q", i, got, firstID)
		}
	}
}

func TestConvertGeminiRequestToOpenAI_ExplicitCallInheritedByImplicitResponse(t *testing.T) {
	inputJSON := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "lookup", "id": "explicit_call_1", "args": {"q": "foo"}}}
				]
			},
			{
				"role": "function",
				"parts": [
					{"functionResponse": {"name": "lookup", "response": {"result": "bar"}}}
				]
			}
		]
	}`)

	out := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	if got := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String(); got != "explicit_call_1" {
		t.Fatalf("tool call ID = %q, want explicit_call_1", got)
	}
	if got := gjson.GetBytes(out, "messages.1.tool_call_id").String(); got != "explicit_call_1" {
		t.Fatalf("tool response ID = %q, want explicit_call_1", got)
	}
}

func TestConvertGeminiRequestToOpenAI_OutOrderExplicitResponseDoesNotDuplicateID(t *testing.T) {
	// Calls: foo (id=call_1), foo (id=call_2), foo (id=call_3)
	// Responses: 1st response has explicit id=call_2, 2nd and 3rd are implicit.
	// Expected responses order: call_2, call_1, call_3.
	inputJSON := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "foo", "id": "call_1", "args": {"n": 1}}},
					{"functionCall": {"name": "foo", "id": "call_2", "args": {"n": 2}}},
					{"functionCall": {"name": "foo", "id": "call_3", "args": {"n": 3}}}
				]
			},
			{
				"role": "function",
				"parts": [
					{"functionResponse": {"name": "foo", "id": "call_2", "response": {"r": 2}}},
					{"functionResponse": {"name": "foo", "response": {"r": 1}}},
					{"functionResponse": {"name": "foo", "response": {"r": 3}}}
				]
			}
		]
	}`)

	out := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	resp1 := gjson.GetBytes(out, "messages.1.tool_call_id").String()
	resp2 := gjson.GetBytes(out, "messages.2.tool_call_id").String()
	resp3 := gjson.GetBytes(out, "messages.3.tool_call_id").String()

	if resp1 != "call_2" {
		t.Fatalf("first response = %q, want call_2", resp1)
	}
	if resp2 != "call_1" {
		t.Fatalf("second response = %q, want call_1", resp2)
	}
	if resp3 != "call_3" {
		t.Fatalf("third response = %q, want call_3", resp3)
	}
}
