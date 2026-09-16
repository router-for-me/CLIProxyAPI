package common

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestJoinRawArray(t *testing.T) {
	tests := []struct {
		name  string
		items [][]byte
		want  string
	}{
		{name: "empty", want: "[]"},
		{name: "single", items: [][]byte{[]byte(`{"id":1}`)}, want: `[{"id":1}]`},
		{name: "multiple", items: [][]byte{[]byte(`{"id":1}`), []byte(`{"id":2}`)}, want: `[{"id":1},{"id":2}]`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := string(JoinRawArray(test.items)); got != test.want {
				t.Fatalf("JoinRawArray() = %s, want %s", got, test.want)
			}
		})
	}
}

func TestNewRawArrayItems(t *testing.T) {
	if items := NewRawArrayItems(0); items != nil {
		t.Fatalf("NewRawArrayItems(0) = %#v, want nil", items)
	}
	if items := NewRawArrayItems(3); len(items) != 0 || cap(items) != 3 {
		t.Fatalf("NewRawArrayItems(3) len = %d, cap = %d; want len 0, cap 3", len(items), cap(items))
	}
}

func TestSetRawArrayItems(t *testing.T) {
	tests := []struct {
		name  string
		data  string
		path  string
		items [][]byte
		want  string
	}{
		{name: "empty", data: `{"items":[]}`, path: "items", want: `{"items":[]}`},
		{name: "single nested", data: `{"before":1,"request":{"contents":[]},"after":2}`, path: "request.contents", items: [][]byte{[]byte(`{"id":1}`)}, want: `{"before":1,"request":{"contents":[{"id":1}]},"after":2}`},
		{name: "single fallback", data: `{"items":[{"old":1},{"old":2}]}`, path: "items", items: [][]byte{[]byte(`{"id":1}`)}, want: `{"items":[{"id":1}]}`},
		{name: "multiple", data: `{"items":[]}`, path: "items", items: [][]byte{[]byte(`{"id":1}`), []byte(`{"id":2}`)}, want: `{"items":[{"id":1},{"id":2}]}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := SetRawArrayItems([]byte(test.data), test.path, test.items)
			if string(got) != test.want {
				t.Fatalf("SetRawArrayItems() = %s, want %s", got, test.want)
			}
		})
	}
}

func TestSetStringWithoutHTMLEscape(t *testing.T) {
	tests := []struct {
		name  string
		data  string
		path  string
		value string
		want  string
	}{
		{
			name:  "command with redirection and pipe",
			data:  `{"arguments":""}`,
			path:  "arguments",
			value: `gh issue view 5802 --json number,title,body,url,state,labels,assignees 2>&1 | head -100`,
			want:  `{"arguments":"gh issue view 5802 --json number,title,body,url,state,labels,assignees 2>&1 | head -100"}`,
		},
		{
			name:  "json string arguments containing redirection",
			data:  `{"arguments":""}`,
			path:  "arguments",
			value: `{"command": "gh issue view 5802 2>&1 | head -100", "timeout": 60}`,
			want:  `{"arguments":"{\"command\": \"gh issue view 5802 2>&1 | head -100\", \"timeout\": 60}"}`,
		},
		{
			name:  "html tags preserved without escaping",
			data:  `{"type":"function_call","name":"bash","arguments":""}`,
			path:  "arguments",
			value: `{"html": "<tag>&value</tag>"}`,
			want:  `{"type":"function_call","name":"bash","arguments":"{\"html\": \"<tag>&value</tag>\"}"}`,
		},
		{
			name:  "nested path",
			data:  `{"item":{"arguments":""}}`,
			path:  "item.arguments",
			value: `2>&1`,
			want:  `{"item":{"arguments":"2>&1"}}`,
		},
		{
			name:  "empty string",
			data:  `{"arguments":"old"}`,
			path:  "arguments",
			value: ``,
			want:  `{"arguments":""}`,
		},
		{
			name:  "newlines tabs and backslashes",
			data:  `{"arguments":""}`,
			path:  "arguments",
			value: "line1\nline2\t\\path\\to\\file",
			want:  "{\"arguments\":\"line1\\nline2\\t\\\\path\\\\to\\\\file\"}",
		},
		{
			name:  "unicode and emoji",
			data:  `{"arguments":""}`,
			path:  "arguments",
			value: `你好，世界！🚀 <&>`,
			want:  `{"arguments":"你好，世界！🚀 <&>"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, errSet := SetStringWithoutHTMLEscape([]byte(tc.data), tc.path, tc.value)
			if errSet != nil {
				t.Fatalf("SetStringWithoutHTMLEscape() error: %v", errSet)
			}
			if string(got) != tc.want {
				t.Fatalf("SetStringWithoutHTMLEscape() = %s, want %s", string(got), tc.want)
			}

			// Verify that the JSON parses back and decodes to the exact same value (round-trip)
			res := gjson.GetBytes(got, tc.path)
			if !res.Exists() {
				t.Fatalf("path %q does not exist in result: %s", tc.path, string(got))
			}
			if res.String() != tc.value {
				t.Fatalf("round-trip value mismatch: got %q, want %q", res.String(), tc.value)
			}
		})
	}
}
