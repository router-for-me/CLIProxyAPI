package util

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestFlattenClaudeToolResultContent(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		isField bool
	}{
		{
			name:  "joins text blocks with blank lines",
			input: `[{"type":"text","text":"alpha"},{"type":"text","text":"beta"}]`,
			want:  "alpha\n\nbeta",
		},
		{
			name:  "preserves non-text blocks as json",
			input: `[{"type":"text","text":"alpha"},{"type":"image","source":{"type":"base64","media_type":"image/png"}}]`,
			want:  "alpha\n\n{\"type\":\"image\",\"source\":{\"type\":\"base64\",\"media_type\":\"image/png\"}}",
		},
		{
			name:    "returns direct string content",
			input:   `{"content":"plain"}`,
			want:    "plain",
			isField: true,
		},
		{
			name:    "returns object raw for unsupported object",
			input:   `{"content":{"foo":"bar"}}`,
			want:    `{"foo":"bar"}`,
			isField: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result gjson.Result
			if tt.isField {
				result = gjson.Parse(tt.input).Get("content")
			} else {
				result = gjson.Parse(tt.input)
			}
			if got := FlattenClaudeToolResultContent(result); got != tt.want {
				t.Fatalf("FlattenClaudeToolResultContent() = %q, want %q", got, tt.want)
			}
		})
	}
}
