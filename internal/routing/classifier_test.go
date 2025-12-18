package routing

import (
	"testing"
)

func TestClassifyByPath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected RequestType
	}{
		{"chat completions", "/v1/chat/completions", RequestTypeChat},
		{"chat completions no slash", "v1/chat/completions", RequestTypeChat},
		{"nested chat completions", "/api/v1/chat/completions", RequestTypeChat},
		{"completions", "/v1/completions", RequestTypeCompletion},
		{"completions no slash", "v1/completions", RequestTypeCompletion},
		{"embeddings", "/v1/embeddings", RequestTypeEmbedding},
		{"embeddings nested", "/api/v1/embeddings", RequestTypeEmbedding},
		{"messages endpoint", "/v1/messages", RequestTypeChat},
		{"unknown", "/v1/models", RequestTypeOther},
		{"empty", "", RequestTypeOther},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := classifyByPath(tt.path)
			if result != tt.expected {
				t.Errorf("classifyByPath(%q) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		name    string
		s       string
		pattern string
		want    bool
	}{
		{"exact match", "gpt-4", "gpt-4", true},
		{"no match", "gpt-4", "gpt-3", false},
		{"wildcard prefix", "gpt-4o", "gpt-4*", true},
		{"wildcard suffix", "my-gpt-4", "*gpt-4", true},
		{"wildcard middle", "gpt-4-turbo", "gpt-*-turbo", true},
		{"multiple wildcards", "claude-3-sonnet", "claude-*-*", true},
		{"wildcard only", "anything", "*", true},
		{"no wildcard match", "gpt-4", "claude-*", false},
		{"codex pattern", "gpt-5.1-codex", "*-codex*", true},
		{"embedding pattern", "text-embedding-ada-002", "text-embedding-*", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchPattern(tt.s, tt.pattern)
			if got != tt.want {
				t.Errorf("matchPattern(%q, %q) = %v, want %v", tt.s, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestClassifyRequest(t *testing.T) {
	globalConfigMu.Lock()
	globalConfig = DefaultRoutingConfig()
	globalConfigMu.Unlock()

	tests := []struct {
		name     string
		path     string
		model    string
		expected RequestType
	}{
		{"path takes precedence", "/v1/chat/completions", "text-embedding-ada", RequestTypeChat},
		{"model fallback for other path", "/v1/custom", "gpt-4o", RequestTypeChat},
		{"embedding model", "/v1/custom", "text-embedding-3-small", RequestTypeEmbedding},
		{"codex model", "/v1/custom", "gpt-5.1-codex", RequestTypeChat}, // gpt-5* matches first
		{"code-davinci completion", "/v1/custom", "code-davinci-002", RequestTypeCompletion},
		{"unknown everything", "/v1/custom", "unknown-model", RequestTypeOther},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ClassifyRequest(tt.path, tt.model)
			if result != tt.expected {
				t.Errorf("ClassifyRequest(%q, %q) = %v, want %v", tt.path, tt.model, result, tt.expected)
			}
		})
	}
}

func TestRequestTypeIsValid(t *testing.T) {
	tests := []struct {
		rt    RequestType
		valid bool
	}{
		{RequestTypeChat, true},
		{RequestTypeCompletion, true},
		{RequestTypeEmbedding, true},
		{RequestTypeOther, true},
		{RequestType("invalid"), false},
		{RequestType(""), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.rt), func(t *testing.T) {
			if got := tt.rt.IsValid(); got != tt.valid {
				t.Errorf("RequestType(%q).IsValid() = %v, want %v", tt.rt, got, tt.valid)
			}
		})
	}
}
