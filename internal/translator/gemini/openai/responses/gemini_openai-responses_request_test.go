package responses

import (
	"encoding/base64"
	"testing"

	"github.com/tidwall/gjson"
)

const testResponsesGeminiThoughtSignature = "EjQKMgEMOdbHO0Gd+c9Mxk4ELwPGbpCEcp2mFfYYLix2UVtBH3fL8GECc4+JITVnHF4qZDsA"

func TestConvertOpenAIResponsesRequestToGemini_ReasoningSignatureCompatibility(t *testing.T) {
	tests := []struct {
		name          string
		encrypted     string
		wantSignature string
	}{
		{
			name:          "GPT encrypted_content uses Gemini bypass",
			encrypted:     validResponsesGPTReasoningSignature(),
			wantSignature: geminiResponsesThoughtSignature,
		},
		{
			name:          "Gemini encrypted_content is preserved",
			encrypted:     "gemini#" + testResponsesGeminiThoughtSignature,
			wantSignature: testResponsesGeminiThoughtSignature,
		},
		{
			name:          "Missing encrypted_content uses Gemini bypass",
			encrypted:     "",
			wantSignature: geminiResponsesThoughtSignature,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := []byte(`{
				"model": "gpt-5",
				"input": [{
					"type": "reasoning",
					"encrypted_content": "` + tt.encrypted + `",
					"summary": [{"type": "summary_text", "text": "reasoning summary"}]
				}]
			}`)

			output := ConvertOpenAIResponsesRequestToGemini("gemini-3.5-flash", input, false)
			part := gjson.GetBytes(output, "contents.0.parts.0")
			if got := part.Get("thoughtSignature").String(); got != tt.wantSignature {
				t.Fatalf("thoughtSignature = %q, want %q. Output: %s", got, tt.wantSignature, output)
			}
			if got := part.Get("text").String(); got != "reasoning summary" {
				t.Fatalf("thought text = %q, want reasoning summary. Output: %s", got, output)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToGemini_SystemAndDeveloperRoles(t *testing.T) {
	tests := []struct {
		name     string
		role     string
		wantText string
	}{
		{
			name:     "system role",
			role:     "system",
			wantText: "System message text",
		},
		{
			name:     "developer role",
			role:     "developer",
			wantText: "Developer message text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := []byte(`{
				"instructions": "Be a helpful assistant",
				"input": [
					{
						"type": "message",
						"role": "` + tt.role + `",
						"content": [
							{
								"type": "input_text",
								"text": "` + tt.wantText + `"
							}
						]
					},
					{
						"type": "message",
						"role": "user",
						"content": [
							{
								"type": "input_text",
								"text": "Hello"
							}
						]
					}
				]
			}`)

			output := ConvertOpenAIResponsesRequestToGemini("gemini-3.5-flash", input, false)
			result := gjson.ParseBytes(output)

			systemInstruction := result.Get("systemInstruction")
			if !systemInstruction.Exists() {
				t.Fatalf("systemInstruction missing. Output: %s", output)
			}
			parts := systemInstruction.Get("parts")
			if got := parts.Get("#").Int(); got != 2 {
				t.Fatalf("systemInstruction parts = %d, want 2. Output: %s", got, output)
			}
			if got := parts.Get("0.text").String(); got != "Be a helpful assistant" {
				t.Fatalf("first systemInstruction part = %q, want %q. Output: %s", got, "Be a helpful assistant", output)
			}
			if got := parts.Get("1.text").String(); got != tt.wantText {
				t.Fatalf("second systemInstruction part = %q, want %q. Output: %s", got, tt.wantText, output)
			}

			result.Get("contents").ForEach(func(_, value gjson.Result) bool {
				if role := value.Get("role").String(); role == tt.role {
					t.Fatalf("role %q leaked into contents array. Output: %s", tt.role, output)
				}
				return true
			})
		})
	}
}

func validResponsesGPTReasoningSignature() string {
	raw := make([]byte, 1+8+16+16+32)
	raw[0] = 0x80
	raw[8] = 1
	for i := 9; i < len(raw); i++ {
		raw[i] = byte(i)
	}
	return base64.URLEncoding.EncodeToString(raw)
}
