package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToAntigravitySkipsEmptyTextPartsWithoutNulls(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": ""},
					{"type": "input_audio", "input_audio": {"data": "SUQzBA==", "format": "mp3"}}
				]
			},
			{
				"role": "assistant",
				"content": [{"type": "text", "text": ""}],
				"tool_calls": [{
					"id": "call_1",
					"type": "function",
					"function": {"name": "read_file", "arguments": "{\"path\":\"a.txt\"}"}
				}]
			},
			{"role": "tool", "tool_call_id": "call_1", "content": "{\"output\":\"ok\"}"},
			{"role": "user", "content": "done"}
		]
	}`

	result := ConvertOpenAIRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)
	userParts := gjson.GetBytes(result, "request.contents.0.parts").Array()
	if len(userParts) != 1 {
		t.Fatalf("user parts length = %d, want 1. Output: %s", len(userParts), result)
	}
	if userParts[0].Type == gjson.Null {
		t.Fatalf("user parts.0 is null. Output: %s", result)
	}
	if got := userParts[0].Get("inlineData.mime_type").String(); got != "audio/mpeg" {
		t.Fatalf("audio mime_type = %q, want audio/mpeg. Output: %s", got, result)
	}

	assistantParts := gjson.GetBytes(result, "request.contents.1.parts").Array()
	if len(assistantParts) != 1 {
		t.Fatalf("assistant parts length = %d, want 1. Output: %s", len(assistantParts), result)
	}
	if assistantParts[0].Type == gjson.Null {
		t.Fatalf("assistant parts.0 is null. Output: %s", result)
	}
	if !assistantParts[0].Get("functionCall").Exists() {
		t.Fatalf("functionCall missing. Output: %s", result)
	}
}

func TestConvertOpenAIRequestToAntigravityThinkingAliases(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "Default Gemini include thoughts",
			body: `{
				"model":"gemini-3.1-pro-low",
				"messages":[{"role":"user","content":"hi"}]
			}`,
			want: true,
		},
		{
			name: "GenerationConfig snake include thoughts",
			body: `{
				"model":"gemini-3.1-pro-low",
				"messages":[{"role":"user","content":"hi"}],
				"generationConfig":{"thinkingConfig":{"include_thoughts":true}}
			}`,
			want: true,
		},
		{
			name: "Top-level thinking include thoughts",
			body: `{
				"model":"gemini-3.1-pro-low",
				"messages":[{"role":"user","content":"hi"}],
				"thinking":{"include_thoughts":true}
			}`,
			want: true,
		},
		{
			name: "Reasoning exclude false includes thoughts",
			body: `{
				"model":"gemini-3.1-pro-low",
				"messages":[{"role":"user","content":"hi"}],
				"reasoning":{"exclude":false}
			}`,
			want: true,
		},
		{
			name: "Reasoning exclude true hides thoughts",
			body: `{
				"model":"gemini-3.1-pro-low",
				"messages":[{"role":"user","content":"hi"}],
				"reasoning":{"exclude":true}
			}`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertOpenAIRequestToAntigravity("gemini-3.1-pro-low", []byte(tt.body), false)
			includeThoughts := gjson.GetBytes(result, "request.generationConfig.thinkingConfig.includeThoughts")
			if !includeThoughts.Exists() {
				t.Fatalf("includeThoughts missing. Output: %s", result)
			}
			if got := includeThoughts.Bool(); got != tt.want {
				t.Fatalf("includeThoughts = %v, want %v. Output: %s", got, tt.want, result)
			}
			if snake := gjson.GetBytes(result, "request.generationConfig.thinkingConfig.include_thoughts"); snake.Exists() {
				t.Fatalf("include_thoughts should be normalized away. Output: %s", result)
			}
		})
	}
}
