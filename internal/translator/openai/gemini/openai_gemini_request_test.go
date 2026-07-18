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
