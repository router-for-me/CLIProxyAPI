package util

import "testing"

func TestStripMarkdownCodeFences(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "json fence",
			in:   "```json\n{\"facts\": [1, 2, 3]}\n```",
			want: "{\"facts\": [1, 2, 3]}",
		},
		{
			name: "xml fence",
			in:   "```xml\n<root><item>hello</item></root>\n```",
			want: "<root><item>hello</item></root>",
		},
		{
			name: "fence without language tag",
			in:   "```\n{\"key\": \"value\"}\n```",
			want: "{\"key\": \"value\"}",
		},
		{
			name: "plain text unchanged",
			in:   "{\"key\": \"value\"}",
			want: "{\"key\": \"value\"}",
		},
		{
			name: "empty string",
			in:   "",
			want: "",
		},
		{
			name: "trailing newlines after closing fence",
			in:   "```json\n{\"a\": 1}\n```\n\n\n",
			want: "{\"a\": 1}",
		},
		{
			name: "leading whitespace before fence",
			in:   "  \n```json\n{\"a\": 1}\n```\n",
			want: "{\"a\": 1}",
		},
		{
			name: "multiline content",
			in:   "```json\n{\n  \"a\": 1,\n  \"b\": 2\n}\n```",
			want: "{\n  \"a\": 1,\n  \"b\": 2\n}",
		},
		{
			name: "backticks inside content not stripped",
			in:   "some text with ``` in the middle",
			want: "some text with ``` in the middle",
		},
		{
			name: "only opening fence no closing",
			in:   "```json\n{\"a\": 1}",
			want: "```json\n{\"a\": 1}",
		},
		{
			name: "single line backticks",
			in:   "```something```",
			want: "```something```",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripMarkdownCodeFences(tt.in)
			if got != tt.want {
				t.Errorf("StripMarkdownCodeFences(%q)\n got: %q\nwant: %q", tt.in, got, tt.want)
			}
		})
	}
}
