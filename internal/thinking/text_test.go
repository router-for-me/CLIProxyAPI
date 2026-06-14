package thinking

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestGetThinkingText(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{
			name: "string thinking field",
			json: `{"thinking":"I am thinking"}`,
			want: "I am thinking",
		},
		{
			name: "string text field (gemini style)",
			json: `{"text":"hello world"}`,
			want: "hello world",
		},
		{
			name: "nested thinking.text",
			json: `{"thinking":{"text":"deep thought","cache_control":{"type":"ephemeral"}}}`,
			want: "deep thought",
		},
		{
			name: "nested thinking.thinking",
			json: `{"thinking":{"thinking":"inner thought"}}`,
			want: "inner thought",
		},
		{
			name: "gemini thought field",
			json: `{"thought":true,"text":"thinking text"}`,
			want: "thinking text",
		},
		{
			name: "no thinking or text field",
			json: `{"role":"user","content":"hi"}`,
			want: "",
		},
		{
			name: "thinking is object but no inner text",
			json: `{"thinking":{"type":"enabled"}}`,
			want: "",
		},
		{
			name: "empty object",
			json: `{}`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := gjson.Parse(tt.json)
			got := GetThinkingText(result)
			if got != tt.want {
				t.Errorf("GetThinkingText() = %q, want %q", got, tt.want)
			}
		})
	}
}
