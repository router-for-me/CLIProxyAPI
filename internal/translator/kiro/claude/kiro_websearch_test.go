package claude

import (
	"testing"
	"strings"
)

func TestHasWebSearchTool(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "pure web search",
			body: `{"tools":[{"name":"web_search"}]}`,
			want: true,
		},
		{
			name: "web search with type",
			body: `{"tools":[{"type":"web_search_20250305"}]}`,
			want: true,
		},
		{
			name: "multiple tools",
			body: `{"tools":[{"name":"web_search"},{"name":"other"}]}`,
			want: false,
		},
		{
			name: "no web search",
			body: `{"tools":[{"name":"other"}]}`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasWebSearchTool([]byte(tt.body)); got != tt.want {
				t.Errorf("HasWebSearchTool() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractSearchQuery(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"Perform a web search for the query: hello world"}]}`
	got := ExtractSearchQuery([]byte(body))
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestFormatSearchContextPrompt(t *testing.T) {
	snippet := "snippet"
	results := &WebSearchResults{
		Results: []WebSearchResult{
			{Title: "title1", URL: "url1", Snippet: &snippet},
		},
	}
	got := FormatSearchContextPrompt("query", results)
	if !strings.Contains(got, "title1") || !strings.Contains(got, "url1") || !strings.Contains(got, "snippet") {
		t.Errorf("unexpected prompt content: %s", got)
	}
}

func TestGenerateWebSearchEvents(t *testing.T) {
	events := GenerateWebSearchEvents("model", "query", "id", nil, 10)
	if len(events) < 11 {
		t.Errorf("expected at least 11 events, got %d", len(events))
	}
	
	foundMessageStart := false
	for _, e := range events {
		if e.Event == "message_start" {
			foundMessageStart = true
			break
		}
	}
	if !foundMessageStart {
		t.Error("message_start event not found")
	}
}
