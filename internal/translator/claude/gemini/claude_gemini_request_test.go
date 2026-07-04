package gemini

import (
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiRequestToClaude_PreservesCustomToolIDs(t *testing.T) {
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

			out := ConvertGeminiRequestToClaude("claude-sonnet-4", raw, false)

			gotCallID := gjson.GetBytes(out, "messages.0.content.0.id").String()
			if gotCallID != tt.want {
				t.Fatalf("expected tool_use id %q, got %q; output=%s", tt.want, gotCallID, string(out))
			}

			gotResultID := gjson.GetBytes(out, "messages.1.content.0.tool_use_id").String()
			if gotResultID != tt.want {
				t.Fatalf("expected tool_result tool_use_id %q, got %q; output=%s", tt.want, gotResultID, string(out))
			}
		})
	}
}

func TestConvertGeminiRequestToClaude_DropsTemperature(t *testing.T) {
	raw := []byte(`{
		"generationConfig": {
			"temperature": 0.2,
			"topP": 0.8
		},
		"contents": [
			{
				"role": "user",
				"parts": [{"text": "hi"}]
			}
		]
	}`)

	out := ConvertGeminiRequestToClaude("claude-sonnet-5", raw, false)

	if gjson.GetBytes(out, "temperature").Exists() {
		t.Fatalf("temperature should be removed")
	}
	if got := gjson.GetBytes(out, "top_p").Float(); got != 0.8 {
		t.Fatalf("top_p = %v, want 0.8", got)
	}
}
