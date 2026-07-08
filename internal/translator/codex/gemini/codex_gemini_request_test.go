package gemini

import (
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiRequestToCodex_PreservesCustomCallIDs(t *testing.T) {
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
			raw := []byte(fmt.Sprintf(`{
				"contents": [
					{
						"role": "model",
						"parts": [
							{"functionCall": {"name": "lookup", %s, "args": {"query": "status"}}}
						]
					},
					{
						"role": "user",
						"parts": [
							{"functionResponse": {"name": "lookup", %s, "response": {"result": "ok"}}}
						]
					}
				]
			}`, tt.callField, tt.responseField))

			out := ConvertGeminiRequestToCodex("gpt-5.1-codex", raw, false)

			gotCallID := gjson.GetBytes(out, "input.0.call_id").String()
			if gotCallID != tt.want {
				t.Fatalf("expected function_call call_id %q, got %q; output=%s", tt.want, gotCallID, string(out))
			}

			gotOutputID := gjson.GetBytes(out, "input.1.call_id").String()
			if gotOutputID != tt.want {
				t.Fatalf("expected function_call_output call_id %q, got %q; output=%s", tt.want, gotOutputID, string(out))
			}
		})
	}
}

func TestConvertGeminiRequestToCodex_AcceptsInlineData(t *testing.T) {
	out := ConvertGeminiRequestToCodex("gpt-5.1-codex", []byte(`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]}]}`), false)
	if got := gjson.GetBytes(out, "input.0.content.0.type").String(); got != "input_image" {
		t.Fatalf("content type = %q, want input_image. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.content.0.image_url").String(); got != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("image_url = %q, want data:image/png;base64,aGVsbG8=. Output: %s", got, string(out))
	}
}

func TestConvertGeminiRequestToCodex_SplitsNonImageInlineDataByMIME(t *testing.T) {
	out := ConvertGeminiRequestToCodex("gpt-5.1-codex", []byte(`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"audio/wav","data":"UklGRg=="}},{"inlineData":{"mimeType":"video/mp4","data":"AAAAIGZ0eXA="}},{"inlineData":{"mimeType":"application/pdf","data":"JVBERi0="}}]}]}`), false)

	if got := gjson.GetBytes(out, "input.0.content.0.type").String(); got != "input_audio" {
		t.Fatalf("audio content type = %q, want input_audio. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.1.content.0.type").String(); got != "input_file" {
		t.Fatalf("video content type = %q, want input_file. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.2.content.0.type").String(); got != "input_file" {
		t.Fatalf("document content type = %q, want input_file. Output: %s", got, string(out))
	}
}
