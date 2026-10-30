package claude

import (
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToGemini_PreservesToolResultErrors(t *testing.T) {
	tests := []struct {
		name         string
		fields       string
		wantResponse string
		wantImage    bool
	}{
		{
			name:         "success_without_flag",
			fields:       `"content":"permission denied"`,
			wantResponse: `{"result":"permission denied"}`,
		},
		{
			name:         "explicit_success",
			fields:       `"is_error":false,"content":{"ok":true}`,
			wantResponse: `{"result":{"ok":true}}`,
		},
		{
			name:         "failed_string",
			fields:       `"is_error":true,"content":"permission denied"`,
			wantResponse: `{"error":"permission denied"}`,
		},
		{
			name:         "failed_structured_result",
			fields:       `"is_error":true,"content":{"code":"EACCES","retryable":false}`,
			wantResponse: `{"error":{"code":"EACCES","retryable":false}}`,
		},
		{
			name:         "failed_text_and_image",
			fields:       `"is_error":true,"content":[{"type":"text","text":"permission denied"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]`,
			wantResponse: `{"error":{"type":"text","text":"permission denied"}}`,
			wantImage:    true,
		},
		{
			name:         "failed_multiple_blocks",
			fields:       `"is_error":true,"content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]`,
			wantResponse: `{"error":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}`,
		},
		{
			name:         "failed_without_content",
			fields:       `"is_error":true`,
			wantResponse: `{"error":""}`,
		},
		{
			name:         "failed_schema_ref_remains_opaque",
			fields:       `"is_error":true,"content":{"$ref":"#/definitions/Error"}`,
			wantResponse: `{"error":"{\"$ref\":\"#/definitions/Error\"}"}`,
		},
	}
	for _, entrypoint := range []struct {
		name      string
		translate func(string, []byte, bool) ([]byte, error)
	}{
		{"native", ConvertClaudeRequestToGemini},
		{"compat", ConvertClaudeRequestToGeminiWithCompat},
	} {
		for _, stream := range []bool{false, true} {
			for _, tt := range tests {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", entrypoint.name, stream, tt.name), func(t *testing.T) {
					input := []byte(fmt.Sprintf(`{"messages":[
						{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{}}]},
						{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1",%s}]}
					]}`, tt.fields))
					output, errTranslate := entrypoint.translate("gemini-3-flash-preview", input, stream)
					if errTranslate != nil {
						t.Fatal(errTranslate)
					}
					if !gjson.ValidBytes(output) {
						t.Fatalf("invalid translated JSON: %s", output)
					}
					response := gjson.GetBytes(output, "contents.1.parts.0.functionResponse")
					if response.Get("id").String() != "call_1" || response.Get("name").String() != "Read" {
						t.Fatalf("tool result lost its call identity: %s", response.Raw)
					}
					if got := response.Get("response").Raw; got != tt.wantResponse {
						t.Fatalf("functionResponse.response = %s, want %s", got, tt.wantResponse)
					}
					parts := gjson.GetBytes(output, "contents.1.parts").Array()
					wantParts := 1
					if tt.wantImage {
						wantParts++
						if got := gjson.GetBytes(output, "contents.1.parts.1.inline_data").Raw; got != `{"mime_type":"image/png","data":"aGVsbG8="}` {
							t.Fatalf("tool image changed: %s", got)
						}
					}
					if len(parts) != wantParts {
						t.Fatalf("tool result parts = %d, want %d", len(parts), wantParts)
					}
				})
			}
		}
	}
}
