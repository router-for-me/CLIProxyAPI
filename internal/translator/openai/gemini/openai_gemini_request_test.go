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

func TestConvertGeminiRequestToOpenAI_Deterministic100Invocations(t *testing.T) {
	inputJSON := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "search", "args": {"q": "golang"}}},
					{"functionCall": {"name": "fetch", "args": {"url": "https://example.com"}}}
				]
			},
			{
				"role": "function",
				"parts": [
					{"functionResponse": {"name": "search", "response": {"results": ["a", "b"]}}},
					{"functionResponse": {"name": "fetch", "response": {"body": "hello"}}}
				]
			}
		]
	}`)

	firstOut := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	for i := 0; i < 100; i++ {
		out := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
		if string(out) != string(firstOut) {
			t.Fatalf("invocation %d produced different bytes:\ngot:  %s\nwant: %s", i, string(out), string(firstOut))
		}
	}
}

func TestConvertGeminiRequestToOpenAI_DuplicateCallsGetDistinctStableIDs(t *testing.T) {
	inputJSON := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "ping", "args": {"host": "localhost"}}},
					{"functionCall": {"name": "ping", "args": {"host": "localhost"}}}
				]
			}
		]
	}`)

	out1 := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	id1 := gjson.GetBytes(out1, "messages.0.tool_calls.0.id").String()
	id2 := gjson.GetBytes(out1, "messages.0.tool_calls.1.id").String()

	if id1 == "" || id2 == "" {
		t.Fatalf("expected non-empty IDs, got id1=%q, id2=%q", id1, id2)
	}
	if id1 == id2 {
		t.Fatalf("expected distinct IDs for duplicate calls, got id1 == id2 == %q", id1)
	}

	out2 := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	if string(out1) != string(out2) {
		t.Fatalf("duplicate call translation is not deterministic across runs:\nout1: %s\nout2: %s", string(out1), string(out2))
	}
}

func TestConvertGeminiRequestToOpenAI_SameNameResponsesConsumeFIFO(t *testing.T) {
	inputJSON := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "fnA", "args": {"step": 1}}},
					{"functionCall": {"name": "fnA", "args": {"step": 2}}}
				]
			},
			{
				"role": "function",
				"parts": [
					{"functionResponse": {"name": "fnA", "response": {"res": 1}}},
					{"functionResponse": {"name": "fnA", "response": {"res": 2}}}
				]
			}
		]
	}`)

	out := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	callID1 := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String()
	callID2 := gjson.GetBytes(out, "messages.0.tool_calls.1.id").String()

	respID1 := gjson.GetBytes(out, "messages.1.tool_call_id").String()
	respID2 := gjson.GetBytes(out, "messages.2.tool_call_id").String()

	if respID1 != callID1 {
		t.Fatalf("first response tool_call_id = %q, want callID1 %q", respID1, callID1)
	}
	if respID2 != callID2 {
		t.Fatalf("second response tool_call_id = %q, want callID2 %q", respID2, callID2)
	}
}

func TestConvertGeminiRequestToOpenAI_InterleavedFunctionsPairByNameAndFIFO(t *testing.T) {
	inputJSON := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "fnA", "args": {"id": 1}}},
					{"functionCall": {"name": "fnB", "args": {"id": 1}}},
					{"functionCall": {"name": "fnA", "args": {"id": 2}}}
				]
			},
			{
				"role": "function",
				"parts": [
					{"functionResponse": {"name": "fnB", "response": {"out": 1}}},
					{"functionResponse": {"name": "fnA", "response": {"out": 1}}},
					{"functionResponse": {"name": "fnA", "response": {"out": 2}}}
				]
			}
		]
	}`)

	out := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	fnA_call1 := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String()
	fnB_call1 := gjson.GetBytes(out, "messages.0.tool_calls.1.id").String()
	fnA_call2 := gjson.GetBytes(out, "messages.0.tool_calls.2.id").String()

	resp_fnB1 := gjson.GetBytes(out, "messages.1.tool_call_id").String()
	resp_fnA1 := gjson.GetBytes(out, "messages.2.tool_call_id").String()
	resp_fnA2 := gjson.GetBytes(out, "messages.3.tool_call_id").String()

	if resp_fnB1 != fnB_call1 {
		t.Fatalf("fnB response paired with %q, want %q", resp_fnB1, fnB_call1)
	}
	if resp_fnA1 != fnA_call1 {
		t.Fatalf("first fnA response paired with %q, want %q", resp_fnA1, fnA_call1)
	}
	if resp_fnA2 != fnA_call2 {
		t.Fatalf("second fnA response paired with %q, want %q", resp_fnA2, fnA_call2)
	}
}

func TestConvertGeminiRequestToOpenAI_ExplicitIDsPreserved(t *testing.T) {
	inputJSON := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "calc", "id": "call_explicit_100", "args": {"x": 5}}}
				]
			},
			{
				"role": "function",
				"parts": [
					{"functionResponse": {"name": "calc", "id": "call_explicit_100", "response": {"ans": 10}}}
				]
			}
		]
	}`)

	out := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	callID := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String()
	respID := gjson.GetBytes(out, "messages.1.tool_call_id").String()

	if callID != "call_explicit_100" {
		t.Fatalf("callID = %q, want %q", callID, "call_explicit_100")
	}
	if respID != "call_explicit_100" {
		t.Fatalf("respID = %q, want %q", respID, "call_explicit_100")
	}
}

func TestConvertGeminiRequestToOpenAI_UnmatchedResponseGetsStableStandaloneID(t *testing.T) {
	inputJSON := []byte(`{
		"contents": [
			{
				"role": "function",
				"parts": [
					{"functionResponse": {"name": "orphan_func", "response": {"status": "ok"}}}
				]
			}
		]
	}`)

	out1 := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	id1 := gjson.GetBytes(out1, "messages.0.tool_call_id").String()

	if !strings.HasPrefix(id1, "call_") {
		t.Fatalf("standalone ID = %q, want call_ prefix", id1)
	}

	out2 := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	id2 := gjson.GetBytes(out2, "messages.0.tool_call_id").String()

	if id1 != id2 {
		t.Fatalf("standalone response ID is not stable: id1=%q, id2=%q", id1, id2)
	}
}

func TestConvertGeminiRequestToOpenAI_MalformedFieldsNoPanicDeterministic(t *testing.T) {
	malformedInputs := [][]byte{
		[]byte(`{"contents":[{"role":"model","parts":[{"functionCall":{}}]}]}`),
		[]byte(`{"contents":[{"role":"function","parts":[{"functionResponse":{}}]}]}`),
		[]byte(`{"contents":[{"role":"model","parts":[{"functionCall":{"name":123,"args":"invalid"}}]}]}`),
		[]byte(`{"contents":[{"role":"function","parts":[{"functionResponse":{"name":true,"response":null}}]}]}`),
	}

	for i, input := range malformedInputs {
		out1 := ConvertGeminiRequestToOpenAI("test-model", input, false)
		out2 := ConvertGeminiRequestToOpenAI("test-model", input, false)
		if string(out1) != string(out2) {
			t.Fatalf("malformed input index %d is not deterministic:\nout1: %s\nout2: %s", i, string(out1), string(out2))
		}
	}
}

func TestConvertGeminiRequestToOpenAI_RealisticCallResultRoundTrip(t *testing.T) {
	inputJSON := []byte(`{
		"contents": [
			{
				"role": "user",
				"parts": [{"text": "What is the weather in Tokyo?"}]
			},
			{
				"role": "model",
				"parts": [
					{"text": "Checking weather..."},
					{"functionCall": {"name": "get_weather", "args": {"city": "Tokyo"}}}
				]
			},
			{
				"role": "function",
				"parts": [
					{"functionResponse": {"name": "get_weather", "response": {"temp": "22C", "condition": "Sunny"}}}
				]
			}
		]
	}`)

	out := ConvertGeminiRequestToOpenAI("gpt-4o", inputJSON, false)

	modelMsgRole := gjson.GetBytes(out, "messages.1.role").String()
	if modelMsgRole != "assistant" {
		t.Fatalf("messages.1.role = %q, want assistant", modelMsgRole)
	}

	callID := gjson.GetBytes(out, "messages.1.tool_calls.0.id").String()
	if callID == "" || !strings.HasPrefix(callID, "call_") {
		t.Fatalf("callID = %q, want valid call_ prefix", callID)
	}

	toolMsgRole := gjson.GetBytes(out, "messages.2.role").String()
	if toolMsgRole != "tool" {
		t.Fatalf("messages.2.role = %q, want tool", toolMsgRole)
	}

	toolMsgCallID := gjson.GetBytes(out, "messages.2.tool_call_id").String()
	if toolMsgCallID != callID {
		t.Fatalf("tool message tool_call_id = %q, want matching callID %q", toolMsgCallID, callID)
	}
}

func TestConvertGeminiRequestToOpenAI_MultiPartToolResponseFollowedByUnmatchedResponse(t *testing.T) {
	inputJSON := []byte(`{
		"contents": [
			{
				"role": "user",
				"parts": [{"text": "Hello"}]
			},
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "funcA", "args": {"x": 1}}}
				]
			},
			{
				"role": "function",
				"parts": [
					{"functionResponse": {"name": "funcA", "response": {"res": 1}}},
					{"functionResponse": {"name": "unmatchedA", "response": {"res": 2}}}
				]
			},
			{
				"role": "function",
				"parts": [
					{"functionResponse": {"name": "unmatchedB", "response": {"res": 3}}}
				]
			}
		]
	}`)

	out1 := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)
	out2 := ConvertGeminiRequestToOpenAI("test-model", inputJSON, false)

	if string(out1) != string(out2) {
		t.Fatalf("multi-part response translation not deterministic across runs")
	}

	unmatched1_ID := gjson.GetBytes(out1, "messages.3.tool_call_id").String()
	unmatched2_ID := gjson.GetBytes(out1, "messages.5.tool_call_id").String()

	if unmatched1_ID == "" || unmatched2_ID == "" {
		t.Fatalf("expected non-empty standalone tool_call_ids, got %q and %q", unmatched1_ID, unmatched2_ID)
	}
	if unmatched1_ID == unmatched2_ID {
		t.Fatalf("expected distinct standalone tool_call_ids across messages, got %q", unmatched1_ID)
	}
}
