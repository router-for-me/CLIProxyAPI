package helps

import (
	"net/http"
	"strings"
	"testing"
)

func TestApplyOpenRouterSessionAffinity(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		header     string
		payload    string
		wantHeader string
	}{
		{name: "OpenRouter uses prompt cache key", url: "https://openrouter.ai/api/v1/chat/completions", payload: `{"prompt_cache_key":"cache-123"}`, wantHeader: "cache-123"},
		{name: "OpenRouter subdomain uses prompt cache key", url: "https://eu.openrouter.ai/api/v1/chat/completions", payload: `{"prompt_cache_key":"cache-123"}`, wantHeader: "cache-123"},
		{name: "other compatibility endpoint is unchanged", url: "https://api.example.com/v1/chat/completions", payload: `{"prompt_cache_key":"cache-123"}`},
		{name: "lookalike endpoint is unchanged", url: "https://openrouter.ai.example.com/v1/chat/completions", payload: `{"prompt_cache_key":"cache-123"}`},
		{name: "explicit header wins", url: "https://openrouter.ai/api/v1/chat/completions", header: "caller-session", payload: `{"prompt_cache_key":"cache-123"}`, wantHeader: "caller-session"},
		{name: "blank key is ignored", url: "https://openrouter.ai/api/v1/chat/completions", payload: `{"prompt_cache_key":"  "}`},
		{name: "control-bearing key is ignored", url: "https://openrouter.ai/api/v1/chat/completions", payload: `{"prompt_cache_key":"cache\nkey"}`},
		{name: "oversized key is ignored", url: "https://openrouter.ai/api/v1/chat/completions", payload: `{"prompt_cache_key":"` + strings.Repeat("a", 257) + `"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, test.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-Session-ID", test.header)

			ApplyOpenRouterSessionAffinity(req, []byte(test.payload))

			if got := req.Header.Get("X-Session-ID"); got != test.wantHeader {
				t.Fatalf("X-Session-ID = %q, want %q", got, test.wantHeader)
			}
		})
	}
}
