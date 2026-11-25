package gemini

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
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

	out, _ := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	firstID := ojson.GetBytesNoCopy(out, "messages.0.tool_calls.0.id").String()
	secondID := ojson.GetBytesNoCopy(out, "messages.0.tool_calls.1.id").String()
	thirdID := ojson.GetBytesNoCopy(out, "messages.0.tool_calls.2.id").String()

	if firstID == "" || secondID == "" || thirdID == "" {
		t.Fatalf("expected all assistant tool call IDs to be set. Output: %s", string(out))
	}
	if firstID == secondID || secondID == thirdID || firstID == thirdID {
		t.Fatalf("expected distinct assistant tool call IDs, got %q, %q, %q", firstID, secondID, thirdID)
	}
	if got := ojson.GetBytesNoCopy(out, "messages.1.tool_call_id").String(); got != firstID {
		t.Fatalf("messages.1.tool_call_id = %q, want %q. Output: %s", got, firstID, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "messages.2.tool_call_id").String(); got != secondID {
		t.Fatalf("messages.2.tool_call_id = %q, want %q. Output: %s", got, secondID, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "messages.3.tool_call_id").String(); got != thirdID {
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

	out, _ := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	toolCallID := ojson.GetBytesNoCopy(out, "messages.0.tool_call_id").String()
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

	out, _ := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	callID := ojson.GetBytesNoCopy(out, "messages.0.tool_calls.0.id").String()
	firstResponseID := ojson.GetBytesNoCopy(out, "messages.1.tool_call_id").String()
	extraResponseID := ojson.GetBytesNoCopy(out, "messages.2.tool_call_id").String()

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

			out, _ := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
			if got := ojson.GetBytesNoCopy(out, "messages.0.tool_calls.0.id").String(); got != tt.want {
				t.Fatalf("tool call id = %q, want %q. Output: %s", got, tt.want, string(out))
			}
			if got := ojson.GetBytesNoCopy(out, "messages.1.tool_call_id").String(); got != tt.want {
				t.Fatalf("tool response id = %q, want %q. Output: %s", got, tt.want, string(out))
			}
		})
	}
}

func TestConvertGeminiRequestToOpenAI_AcceptsSnakeInlineData(t *testing.T) {
	out, _ := ConvertGeminiRequestToOpenAI("gpt-test", []byte(`{"contents":[{"role":"user","parts":[{"inline_data":{"mime_type":"image/png","data":"aGVsbG8="}}]}]}`), false)
	if got := ojson.GetBytesNoCopy(out, "messages.0.content.0.image_url.url").String(); got != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("image url = %q, want data:image/png;base64,aGVsbG8=. Output: %s", got, string(out))
	}
}

func TestConvertGeminiRequestToOpenAI_SplitsNonImageInlineDataByMIME(t *testing.T) {
	out, _ := ConvertGeminiRequestToOpenAI("gpt-test", []byte(`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"audio/wav","data":"UklGRg=="}},{"inlineData":{"mimeType":"video/mp4","data":"AAAAIGZ0eXA="}},{"inlineData":{"mimeType":"application/pdf","data":"JVBERi0="}}]}]}`), false)

	if got := ojson.GetBytesNoCopy(out, "messages.0.content.0.type").String(); got != "input_audio" {
		t.Fatalf("audio content type = %q, want input_audio. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "messages.0.content.1.type").String(); got != "video_url" {
		t.Fatalf("video content type = %q, want video_url. Output: %s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "messages.0.content.2.type").String(); got != "file" {
		t.Fatalf("document content type = %q, want file. Output: %s", got, string(out))
	}
	if ojson.GetBytesNoCopy(out, "messages.0.content.#(type==\"image_url\")").Exists() {
		t.Fatalf("non-image inlineData must not be converted to image_url. Output: %s", string(out))
	}
}

func TestConvertGeminiRequestToOpenAI_DropsHiddenThoughtParts(t *testing.T) {
	t.Run("thought-only turn", func(t *testing.T) {
		out, _ := ConvertGeminiRequestToOpenAI("openai-test", []byte(`{
			"contents":[
				{"role":"model","parts":[{"thought":true,"text":"internal reasoning","thoughtSignature":"opaque-provider-state"}]},
				{"role":"user","parts":[{"text":"continue"}]}
			]
		}`), false)

		messages := ojson.GetBytesNoCopy(out, "messages").Array()
		if len(messages) != 1 || messages[0].Get("role").String() != "user" || messages[0].Get("content").String() != "continue" {
			t.Fatalf("hidden thought turn was not dropped. Output: %s", string(out))
		}
	})

	t.Run("mixed turn", func(t *testing.T) {
		out, _ := ConvertGeminiRequestToOpenAI("openai-test", []byte(`{
			"contents":[{"role":"model","parts":[
				{"thought":true,"text":"internal reasoning","thoughtSignature":"opaque-provider-state"},
				{"text":"visible answer"}
			]}]
		}`), false)

		messages := ojson.GetBytesNoCopy(out, "messages").Array()
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

	firstOut, _ := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	firstCall0 := ojson.GetBytesNoCopy(firstOut, "messages.0.tool_calls.0.id").String()
	firstCall1 := ojson.GetBytesNoCopy(firstOut, "messages.0.tool_calls.1.id").String()
	firstResp0 := ojson.GetBytesNoCopy(firstOut, "messages.1.tool_call_id").String()
	firstResp1 := ojson.GetBytesNoCopy(firstOut, "messages.2.tool_call_id").String()

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
		out, _ := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
		if got := ojson.GetBytesNoCopy(out, "messages.0.tool_calls.0.id").String(); got != firstCall0 {
			t.Fatalf("iteration %d: tool_calls.0.id = %q, want %q", i, got, firstCall0)
		}
		if got := ojson.GetBytesNoCopy(out, "messages.0.tool_calls.1.id").String(); got != firstCall1 {
			t.Fatalf("iteration %d: tool_calls.1.id = %q, want %q", i, got, firstCall1)
		}
		if got := ojson.GetBytesNoCopy(out, "messages.1.tool_call_id").String(); got != firstResp0 {
			t.Fatalf("iteration %d: messages.1.tool_call_id = %q, want %q", i, got, firstResp0)
		}
		if got := ojson.GetBytesNoCopy(out, "messages.2.tool_call_id").String(); got != firstResp1 {
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

	out, _ := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	id0 := ojson.GetBytesNoCopy(out, "messages.0.tool_calls.0.id").String()
	id1 := ojson.GetBytesNoCopy(out, "messages.0.tool_calls.1.id").String()

	if id0 == id1 {
		t.Fatalf("expected distinct IDs for same-name calls in same message, got both %q", id0)
	}

	resp0 := ojson.GetBytesNoCopy(out, "messages.1.tool_call_id").String()
	resp1 := ojson.GetBytesNoCopy(out, "messages.2.tool_call_id").String()

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

	out, _ := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	callA1 := ojson.GetBytesNoCopy(out, "messages.0.tool_calls.0.id").String()
	callB1 := ojson.GetBytesNoCopy(out, "messages.0.tool_calls.1.id").String()
	callA2 := ojson.GetBytesNoCopy(out, "messages.0.tool_calls.2.id").String()
	callB2 := ojson.GetBytesNoCopy(out, "messages.0.tool_calls.3.id").String()

	// Responses:
	// messages[1] = tool_b (step 1) -> should match callB1
	// messages[2] = tool_a (step 1) -> should match callA1
	// messages[3] = tool_b (step 2) -> should match callB2
	// messages[4] = tool_a (step 2) -> should match callA2
	if got := ojson.GetBytesNoCopy(out, "messages.1.tool_call_id").String(); got != callB1 {
		t.Fatalf("first response (tool_b) = %q, want callB1 %q", got, callB1)
	}
	if got := ojson.GetBytesNoCopy(out, "messages.2.tool_call_id").String(); got != callA1 {
		t.Fatalf("second response (tool_a) = %q, want callA1 %q", got, callA1)
	}
	if got := ojson.GetBytesNoCopy(out, "messages.3.tool_call_id").String(); got != callB2 {
		t.Fatalf("third response (tool_b) = %q, want callB2 %q", got, callB2)
	}
	if got := ojson.GetBytesNoCopy(out, "messages.4.tool_call_id").String(); got != callA2 {
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

	firstOut, _ := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	firstID := ojson.GetBytesNoCopy(firstOut, "messages.0.tool_call_id").String()
	if !strings.HasPrefix(firstID, "call_") {
		t.Fatalf("expected fallback tool_call_id with call_ prefix, got %q", firstID)
	}

	for i := 0; i < 100; i++ {
		out, _ := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
		if got := ojson.GetBytesNoCopy(out, "messages.0.tool_call_id").String(); got != firstID {
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

	out, _ := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	if got := ojson.GetBytesNoCopy(out, "messages.0.tool_calls.0.id").String(); got != "explicit_call_1" {
		t.Fatalf("tool call ID = %q, want explicit_call_1", got)
	}
	if got := ojson.GetBytesNoCopy(out, "messages.1.tool_call_id").String(); got != "explicit_call_1" {
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

	out, _ := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	resp1 := ojson.GetBytesNoCopy(out, "messages.1.tool_call_id").String()
	resp2 := ojson.GetBytesNoCopy(out, "messages.2.tool_call_id").String()
	resp3 := ojson.GetBytesNoCopy(out, "messages.3.tool_call_id").String()

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

func TestConvertGeminiRequestToOpenAI_PreservesFunctionCallThoughtSignature(t *testing.T) {
	inputJSON := []byte(`{
		"contents": [
			{"role": "user", "parts": [{"text": "weather in Paris and London?"}]},
			{
				"role": "model",
				"parts": [
					{
						"functionCall": {"name": "get_weather", "args": {"city": "Paris"}},
						"thoughtSignature": "SIG_PARIS"
					},
					{
						"functionCall": {"name": "get_weather", "args": {"city": "London"}}
					}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "get_weather", "response": {"t": 20}}},
					{"functionResponse": {"name": "get_weather", "response": {"t": 15}}}
				]
			}
		]
	}`)

	out, err := ConvertGeminiRequestToOpenAI("google/gemini-3-pro", inputJSON, false)
	if err != nil {
		t.Fatalf("ConvertGeminiRequestToOpenAI() error = %v", err)
	}
	if got := ojson.GetBytesNoCopy(out, "messages.1.tool_calls.0.extra_content.google.thought_signature").String(); got != "SIG_PARIS" {
		t.Fatalf("messages.1.tool_calls.0.extra_content.google.thought_signature = %q, want SIG_PARIS; out=%s", got, string(out))
	}
	if ojson.GetBytesNoCopy(out, "messages.1.tool_calls.1.extra_content").Exists() {
		t.Fatalf("expected unsigned sibling tool call not to have extra_content; out=%s", string(out))
	}
}

func TestConvertGeminiRequestToOpenAI_FunctionResponseDoesNotAppendTrailingEmptyFunctionMessage(t *testing.T) {
	inputJSON := []byte(`{
		"contents": [
			{"parts": [{"text": "What is the weather?"}]},
			{"parts": [{"functionCall": {"name": "get_weather", "args": {"city": "Tokyo"}}}]},
			{"role": "function", "parts": [{"functionResponse": {"name": "get_weather", "response": {"result": "Sunny"}}}]}
		]
	}`)

	out, err := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	if err != nil {
		t.Fatalf("ConvertGeminiRequestToOpenAI() error = %v", err)
	}
	messages := ojson.GetBytesNoCopy(out, "messages").Array()
	if len(messages) != 3 {
		t.Fatalf("len(messages) = %d, want 3 (user, assistant, tool); out=%s", len(messages), string(out))
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("messages[0].role = %q, want user; out=%s", got, string(out))
	}
	if got := messages[1].Get("role").String(); got != "assistant" {
		t.Fatalf("messages[1].role = %q, want assistant; out=%s", got, string(out))
	}
	if got := messages[2].Get("role").String(); got != "tool" {
		t.Fatalf("messages[2].role = %q, want tool; out=%s", got, string(out))
	}
}

func TestConvertGeminiRequestToOpenAI_DefaultsOmittedContentRoleToUser(t *testing.T) {
	inputJSON := []byte(`{"contents":[{"parts":[{"text":"Reply with exactly: OK"}]},{"role":"invalid","parts":[{"text":"OK"}]},{"parts":[{"function_call":{"name":"ping","args":{"x":1}}}]},{"parts":[{"function_response":{"name":"ping","response":{"result":"pong"}}}]}]}`)

	out, err := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	if err != nil {
		t.Fatalf("ConvertGeminiRequestToOpenAI() error = %v", err)
	}
	messages := ojson.GetBytesNoCopy(out, "messages").Array()
	if len(messages) != 4 {
		t.Fatalf("len(messages) = %d, want 4; out=%s", len(messages), string(out))
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("messages[0].role = %q, want user; out=%s", got, string(out))
	}
	if got := messages[1].Get("role").String(); got != "assistant" {
		t.Fatalf("messages[1].role = %q, want assistant (alternated after user); out=%s", got, string(out))
	}
	if got := messages[2].Get("role").String(); got != "assistant" || messages[2].Get("tool_calls.0.function.name").String() != "ping" {
		t.Fatalf("messages[2] = %s, want assistant tool_call ping", messages[2].Raw)
	}
	if got := messages[3].Get("role").String(); got != "tool" {
		t.Fatalf("messages[3].role = %q, want tool; out=%s", got, string(out))
	}
}

func TestConvertGeminiRequestToOpenAI_FunctionResponsePartsMedia(t *testing.T) {
	t.Run("inlineData image and PDF alongside response relayed in user message", func(t *testing.T) {
		inputJSON := []byte(`{
			"contents": [
				{"role": "user", "parts": [{"text": "Read red.png and doc.pdf"}]},
				{"role": "model", "parts": [{"functionCall": {"name": "read_files", "args": {}}}]},
				{"role": "user", "parts": [{
					"functionResponse": {
						"name": "read_files",
						"response": {"result": "Loaded files"},
						"parts": [
							{"inlineData": {"mimeType": "image/png", "data": "iVBORw0KGgo="}},
							{"inlineData": {"mimeType": "application/pdf", "data": "JVBERi0xLjQK"}}
						]
					}
				}]}
			]
		}`)

		out, err := ConvertGeminiRequestToOpenAI("gpt-5", inputJSON, false)
		if err != nil {
			t.Fatalf("ConvertGeminiRequestToOpenAI() error = %v", err)
		}
		messages := ojson.GetBytesNoCopy(out, "messages").Array()
		if len(messages) != 4 {
			t.Fatalf("len(messages) = %d, want 4 (user, assistant, tool, user relay); out=%s", len(messages), string(out))
		}
		if messages[2].Get("role").String() != "tool" {
			t.Fatalf("messages[2].role = %q, want tool; out=%s", messages[2].Get("role").String(), string(out))
		}
		relay := messages[3]
		if relay.Get("role").String() != "user" {
			t.Fatalf("messages[3].role = %q, want user; out=%s", relay.Get("role").String(), string(out))
		}
		parts := relay.Get("content").Array()
		if len(parts) != 3 {
			t.Fatalf("expected 3 relay parts (notice + image_url + file), got %d: %s", len(parts), relay.Get("content").Raw)
		}
		if parts[0].Get("type").String() != "text" || parts[0].Get("text").String() == "" {
			t.Fatalf("parts[0] = %s, want non-empty relay notice text", parts[0].Raw)
		}
		if parts[1].Get("type").String() != "image_url" || parts[1].Get("image_url.url").String() != "data:image/png;base64,iVBORw0KGgo=" {
			t.Fatalf("parts[1] = %s, want image_url data URL", parts[1].Raw)
		}
		if parts[2].Get("type").String() != "file" || parts[2].Get("file.file_data").String() != "data:application/pdf;base64,JVBERi0xLjQK" || parts[2].Get("file.filename").String() != "document.pdf" {
			t.Fatalf("parts[2] = %s, want file part with PDF data URL", parts[2].Raw)
		}
	})

	t.Run("media-only functionResponse sets placeholder on tool message and merges with trailing user text", func(t *testing.T) {
		inputJSON := []byte(`{
			"contents": [
				{"role": "model", "parts": [{"functionCall": {"name": "read_image", "args": {}}}]},
				{"role": "user", "parts": [
					{"functionResponse": {
						"name": "read_image",
						"response": {},
						"parts": [
							{"fileData": {"mimeType": "image/jpeg", "fileUri": "https://example.com/a.jpg"}}
						]
					}},
					{"text": "What color is it?"}
				]}
			]
		}`)

		out, err := ConvertGeminiRequestToOpenAI("gpt-5", inputJSON, false)
		if err != nil {
			t.Fatalf("ConvertGeminiRequestToOpenAI() error = %v", err)
		}
		messages := ojson.GetBytesNoCopy(out, "messages").Array()
		if len(messages) != 3 {
			t.Fatalf("len(messages) = %d, want 3 (assistant, tool, user); out=%s", len(messages), string(out))
		}
		if messages[1].Get("role").String() != "tool" || messages[1].Get("content").String() == "" || messages[1].Get("content").String() == "{}" {
			t.Fatalf("messages[1] = %s, want non-empty placeholder tool content", messages[1].Raw)
		}
		parts := messages[2].Get("content").Array()
		if len(parts) != 3 {
			t.Fatalf("expected 3 parts in merged user message, got %d: %s", len(parts), messages[2].Get("content").Raw)
		}
		if parts[1].Get("type").String() != "image_url" || parts[1].Get("image_url.url").String() != "https://example.com/a.jpg" {
			t.Fatalf("parts[1] = %s, want image_url", parts[1].Raw)
		}
		if parts[2].Get("type").String() != "text" || parts[2].Get("text").String() != "What color is it?" {
			t.Fatalf("parts[2] = %s, want user text", parts[2].Raw)
		}
	})

	t.Run("parallel tool calls across adjacent contents turns do not split tool messages with relay", func(t *testing.T) {
		inputJSON := []byte(`{
			"contents": [
				{"role": "model", "parts": [
					{"functionCall": {"name": "read_a", "id": "call_a", "args": {}}},
					{"functionCall": {"name": "read_b", "id": "call_b", "args": {}}}
				]},
				{"role": "user", "parts": [{
					"functionResponse": {
						"name": "read_a",
						"id": "call_a",
						"response": {"result": "a"},
						"parts": [
							{"inlineData": {"mimeType": "image/png", "data": "QUFB"}}
						]
					}
				}]},
				{"role": "user", "parts": [{
					"functionResponse": {
						"name": "read_b",
						"id": "call_b",
						"response": {"result": "b"}
					}
				}]}
			]
		}`)

		out, err := ConvertGeminiRequestToOpenAI("gpt-5", inputJSON, false)
		if err != nil {
			t.Fatalf("ConvertGeminiRequestToOpenAI() error = %v", err)
		}
		messages := ojson.GetBytesNoCopy(out, "messages").Array()
		if len(messages) != 4 {
			t.Fatalf("len(messages) = %d, want 4 (assistant, tool, tool, user relay); out=%s", len(messages), string(out))
		}
		if messages[0].Get("role").String() != "assistant" {
			t.Fatalf("messages[0].role = %q, want assistant", messages[0].Get("role").String())
		}
		if messages[1].Get("role").String() != "tool" || messages[1].Get("tool_call_id").String() != "call_a" {
			t.Fatalf("messages[1] = %s, want tool call_a", messages[1].Raw)
		}
		if messages[2].Get("role").String() != "tool" || messages[2].Get("tool_call_id").String() != "call_b" {
			t.Fatalf("messages[2] = %s, want tool call_b before user relay; out=%s", messages[2].Raw, string(out))
		}
		if messages[3].Get("role").String() != "user" || messages[3].Get("content.1.image_url.url").String() != "data:image/png;base64,QUFB" {
			t.Fatalf("messages[3] = %s, want user relay after all tool messages", messages[3].Raw)
		}
	})

	t.Run("parallel tool calls with sibling text in first response turn keep all tool messages contiguous", func(t *testing.T) {
		inputJSON := []byte(`{
			"contents": [
				{"role": "model", "parts": [
					{"functionCall": {"name": "read_a", "id": "call_a", "args": {}}},
					{"functionCall": {"name": "read_b", "id": "call_b", "args": {}}}
				]},
				{"role": "user", "parts": [
					{"functionResponse": {
						"name": "read_a",
						"id": "call_a",
						"response": {"result": "a"},
						"parts": [
							{"inlineData": {"mimeType": "image/png", "data": "QUFB"}}
						]
					}},
					{"text": "check"}
				]},
				{"role": "user", "parts": [{
					"functionResponse": {
						"name": "read_b",
						"id": "call_b",
						"response": {"result": "b"}
					}
				}]}
			]
		}`)

		out, err := ConvertGeminiRequestToOpenAI("gpt-5", inputJSON, false)
		if err != nil {
			t.Fatalf("ConvertGeminiRequestToOpenAI() error = %v", err)
		}
		messages := ojson.GetBytesNoCopy(out, "messages").Array()
		if len(messages) != 4 {
			t.Fatalf("len(messages) = %d, want 4 (assistant, tool, tool, user); out=%s", len(messages), string(out))
		}
		if messages[1].Get("role").String() != "tool" || messages[1].Get("tool_call_id").String() != "call_a" {
			t.Fatalf("messages[1] = %s, want tool call_a", messages[1].Raw)
		}
		if messages[2].Get("role").String() != "tool" || messages[2].Get("tool_call_id").String() != "call_b" {
			t.Fatalf("messages[2] = %s, want tool call_b before user message; out=%s", messages[2].Raw, string(out))
		}
		if messages[3].Get("role").String() != "user" {
			t.Fatalf("messages[3] = %s, want user message after all tool messages", messages[3].Raw)
		}
		userParts := messages[3].Get("content").Array()
		if len(userParts) != 3 || userParts[1].Get("type").String() != "image_url" || userParts[2].Get("text").String() != "check" {
			t.Fatalf("messages[3].content = %s, want [notice, image_url, text 'check']", messages[3].Get("content").Raw)
		}
	})

	t.Run("parallel tool responses tagged with model role keep tool messages contiguous before user relay", func(t *testing.T) {
		inputJSON := []byte(`{
			"contents": [
				{"role": "model", "parts": [
					{"functionCall": {"name": "read_a", "id": "call_a", "args": {}}},
					{"functionCall": {"name": "read_b", "id": "call_b", "args": {}}}
				]},
				{"role": "model", "parts": [{
					"functionResponse": {
						"name": "read_a",
						"id": "call_a",
						"response": {"result": "a"},
						"parts": [
							{"inlineData": {"mimeType": "image/png", "data": "QUFB"}}
						]
					}
				}]},
				{"role": "model", "parts": [{
					"functionResponse": {
						"name": "read_b",
						"id": "call_b",
						"response": {"result": "b"}
					}
				}]}
			]
		}`)

		out, err := ConvertGeminiRequestToOpenAI("gpt-5", inputJSON, false)
		if err != nil {
			t.Fatalf("ConvertGeminiRequestToOpenAI() error = %v", err)
		}
		messages := ojson.GetBytesNoCopy(out, "messages").Array()
		if len(messages) != 4 {
			t.Fatalf("len(messages) = %d, want 4 (assistant, tool, tool, user relay); out=%s", len(messages), string(out))
		}
		if messages[1].Get("role").String() != "tool" || messages[1].Get("tool_call_id").String() != "call_a" {
			t.Fatalf("messages[1] = %s, want tool call_a", messages[1].Raw)
		}
		if messages[2].Get("role").String() != "tool" || messages[2].Get("tool_call_id").String() != "call_b" {
			t.Fatalf("messages[2] = %s, want tool call_b before user relay; out=%s", messages[2].Raw, string(out))
		}
		if messages[3].Get("role").String() != "user" || messages[3].Get("content.1.image_url.url").String() != "data:image/png;base64,QUFB" {
			t.Fatalf("messages[3] = %s, want user relay after all tool messages", messages[3].Raw)
		}
		noticeText := messages[3].Get("content.0.text").String()
		if !strings.Contains(noticeText, "read_a") || !strings.Contains(noticeText, "call_a") {
			t.Fatalf("messages[3].content[0].text = %q, want attribution to read_a and call_a", noticeText)
		}
	})

	t.Run("parallel tool calls with identical text and only one media attachment still preserve tool attribution", func(t *testing.T) {
		inputJSON := []byte(`{
			"contents": [
				{"role": "model", "parts": [
					{"functionCall": {"name": "inspect_a", "id": "call_1", "args": {}}},
					{"functionCall": {"name": "inspect_b", "id": "call_2", "args": {}}}
				]},
				{"role": "user", "parts": [
					{"functionResponse": {
						"name": "inspect_a",
						"id": "call_1",
						"response": {"result": "ok"},
						"parts": [
							{"inlineData": {"mimeType": "image/png", "data": "QUFB"}}
						]
					}},
					{"functionResponse": {
						"name": "inspect_b",
						"id": "call_2",
						"response": {"result": "ok"}
					}}
				]}
			]
		}`)

		out, err := ConvertGeminiRequestToOpenAI("gpt-5", inputJSON, false)
		if err != nil {
			t.Fatalf("ConvertGeminiRequestToOpenAI() error = %v", err)
		}
		messages := ojson.GetBytesNoCopy(out, "messages").Array()
		if len(messages) != 4 {
			t.Fatalf("len(messages) = %d, want 4; out=%s", len(messages), string(out))
		}
		noticeText := messages[3].Get("content.0.text").String()
		if !strings.Contains(noticeText, "inspect_a") || !strings.Contains(noticeText, "call_1") {
			t.Fatalf("relay notice = %q, want attribution to inspect_a and call_1", noticeText)
		}
		if strings.Contains(noticeText, "inspect_b") || strings.Contains(noticeText, "call_2") {
			t.Fatalf("relay notice = %q, must not misattribute inspect_b/call_2", noticeText)
		}
	})

	t.Run("parallel tool media relay preserves per-tool attribution labels when multiple tools return media", func(t *testing.T) {
		inputJSON := []byte(`{
			"contents": [
				{"role": "model", "parts": [
					{"functionCall": {"name": "read_a", "id": "call_a", "args": {}}},
					{"functionCall": {"name": "read_b", "id": "call_b", "args": {}}}
				]},
				{"role": "user", "parts": [
					{"functionResponse": {
						"name": "read_a",
						"id": "call_a",
						"response": {},
						"parts": [
							{"inlineData": {"mimeType": "image/png", "data": "QUFB"}},
							{"inlineData": {"mimeType": "image/png", "data": "QkJC"}}
						]
					}},
					{"functionResponse": {
						"name": "read_b",
						"id": "call_b",
						"response": {},
						"parts": [
							{"inlineData": {"mimeType": "image/png", "data": "Q0ND"}}
						]
					}}
				]}
			]
		}`)

		out, err := ConvertGeminiRequestToOpenAI("gpt-5", inputJSON, false)
		if err != nil {
			t.Fatalf("ConvertGeminiRequestToOpenAI() error = %v", err)
		}
		messages := ojson.GetBytesNoCopy(out, "messages").Array()
		if len(messages) != 4 {
			t.Fatalf("len(messages) = %d, want 4; out=%s", len(messages), string(out))
		}
		relayParts := messages[3].Get("content").Array()
		// Expected: [notice, "Tool read_a (call_a):", img1, img2, "Tool read_b (call_b):", img3]
		if len(relayParts) != 6 {
			t.Fatalf("len(relayParts) = %d, want 6 with per-tool attribution labels; content=%s", len(relayParts), messages[3].Get("content").Raw)
		}
		if relayParts[1].Get("type").String() != "text" || !strings.Contains(relayParts[1].Get("text").String(), "call_a") || !strings.Contains(relayParts[1].Get("text").String(), "read_a") {
			t.Fatalf("relayParts[1] = %s, want label identifying read_a / call_a", relayParts[1].Raw)
		}
		if relayParts[2].Get("image_url.url").String() != "data:image/png;base64,QUFB" || relayParts[3].Get("image_url.url").String() != "data:image/png;base64,QkJC" {
			t.Fatalf("relayParts[2..3] = %s, %s, want QUFB and QkJC under call_a", relayParts[2].Raw, relayParts[3].Raw)
		}
		if relayParts[4].Get("type").String() != "text" || !strings.Contains(relayParts[4].Get("text").String(), "call_b") || !strings.Contains(relayParts[4].Get("text").String(), "read_b") {
			t.Fatalf("relayParts[4] = %s, want label identifying read_b / call_b", relayParts[4].Raw)
		}
		if relayParts[5].Get("image_url.url").String() != "data:image/png;base64,Q0ND" {
			t.Fatalf("relayParts[5] = %s, want Q0ND under call_b", relayParts[5].Raw)
		}
	})
}

func TestConvertGeminiRequestToOpenAI_NormalizesNumericProtoSchemaTypes_Issue8032(t *testing.T) {
	inputJSON := []byte(`{
		"contents": [{"role": "user", "parts": [{"text": "hello"}]}],
		"tools": [{
			"functionDeclarations": [{
				"name": "search",
				"parameters": {
					"type": 6,
					"properties": {
						"query": {"type": 1},
						"tags": {"type": 5, "items": {"type": 1}}
					}
				}
			}]
		}]
	}`)

	out, err := ConvertGeminiRequestToOpenAI("gpt-5", inputJSON, false)
	if err != nil {
		t.Fatalf("ConvertGeminiRequestToOpenAI returned error: %v", err)
	}

	schema := ojson.GetBytesNoCopy(out, "tools.0.function.parameters")
	for _, tc := range []struct {
		path string
		want string
	}{
		{"type", "object"},
		{"properties.query.type", "string"},
		{"properties.tags.type", "array"},
		{"properties.tags.items.type", "string"},
	} {
		got := schema.Get(tc.path)
		if got.Type != ojson.String || got.String() != tc.want {
			t.Errorf("tools.0.function.parameters.%s = %s (type %v), want string %q", tc.path, got.Raw, got.Type, tc.want)
		}
	}
}
