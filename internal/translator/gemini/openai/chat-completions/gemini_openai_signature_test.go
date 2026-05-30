package chat_completions

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/tidwall/gjson"
)

const capturedGeminiToolCallThoughtSignature = "EjQKMgEMOdbHO0Gd+c9Mxk4ELwPGbpCEcp2mFfYYLix2UVtBH3fL8GECc4+JITVnHF4qZDsA"

func TestConvertOpenAIRequestToGemini_ToolCallSignatureCompatibility(t *testing.T) {
	tests := []struct {
		name          string
		rawSignature  string
		wantSignature string
	}{
		{
			name:          "Gemini signature is preserved",
			rawSignature:  "gemini#" + capturedGeminiToolCallThoughtSignature,
			wantSignature: capturedGeminiToolCallThoughtSignature,
		},
		{
			name:          "unknown signature uses bypass",
			rawSignature:  "not-a-provider-signature",
			wantSignature: signature.GeminiSkipThoughtSignatureValidator,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := []byte(`{
				"model": "gemini-3.5-flash",
				"messages": [{
					"role": "assistant",
					"tool_calls": [{
						"id": "call_123",
						"type": "function",
						"function": {"name": "lookup", "arguments": "{\"q\":\"Paris\"}"},
						"extra_content": {"google": {"thought_signature": "` + tt.rawSignature + `"}}
					}]
				}]
			}`)

			output := ConvertOpenAIRequestToGemini("gemini-3.5-flash", input, false)
			if got := gjson.GetBytes(output, "contents.0.parts.0.thoughtSignature").String(); got != tt.wantSignature {
				t.Fatalf("thoughtSignature = %q, want %q. Output: %s", got, tt.wantSignature, output)
			}
		})
	}
}
