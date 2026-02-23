package util

import "testing"

func TestIsWebSearchTool(t *testing.T) {
	tests := []struct {
		title    string
		toolName string
		typ      string
		want     bool
	}{
		{title: "name only", toolName: "web_search", typ: "", want: true},
		{title: "name only mixed case", toolName: "WEB_SEARCH", typ: "", want: true},
		{title: "type exact", toolName: "", typ: "web_search_20250305", want: true},
		{title: "type legacy", toolName: "", typ: "web_search_beta_202501", want: true},
		{title: "not web search", toolName: "other_tool", typ: "other", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			if got := IsWebSearchTool(tt.toolName, tt.typ); got != tt.want {
				t.Fatalf("IsWebSearchTool(%q, %q) = %v, want %v", tt.toolName, tt.typ, got, tt.want)
			}
		})
	}

	for _, tt := range []struct {
		name string
		typ  string
		want bool
	}{
		{name: "empty", typ: "", want: false},
		{name: "type prefix", typ: "web_search_202501", want: true},
	} {
		t.Run("typ-only-"+tt.name, func(t *testing.T) {
			if got := IsWebSearchTool("", tt.typ); got != tt.want {
				t.Fatalf("IsWebSearchTool(\"\", %q) = %v, want %v", tt.typ, got, tt.want)
			}
		})
	}
}
