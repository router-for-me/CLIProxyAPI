package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestBuildRequestURL(t *testing.T) {
	e := &OpenAICompatExecutor{}

	tests := []struct {
		baseURL  string
		wireAPI  string
		expected string
	}{
		{"https://api.example.com/v1", "chat", "https://api.example.com/v1/chat/completions"},
		{"https://api.example.com/v1/", "chat", "https://api.example.com/v1/chat/completions"},
		{"https://api.example.com/v1", "responses", "https://api.example.com/v1/responses"},
		{"https://api.example.com/v1/", "responses", "https://api.example.com/v1/responses"},
		{"https://api.example.com", "", "https://api.example.com/chat/completions"},
	}

	for _, tt := range tests {
		got := e.buildRequestURL(tt.baseURL, tt.wireAPI)
		if got != tt.expected {
			t.Errorf("buildRequestURL(%q, %q) = %q, want %q", tt.baseURL, tt.wireAPI, got, tt.expected)
		}
	}
}

func TestResolveWireAPI(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.Config
		auth     *cliproxyauth.Auth
		expected string
	}{
		{
			name:     "nil auth returns chat",
			cfg:      nil,
			auth:     nil,
			expected: "chat",
		},
		{
			name:     "nil config returns chat",
			cfg:      nil,
			auth:     &cliproxyauth.Auth{Provider: "test"},
			expected: "chat",
		},
		{
			name: "config with responses wire-api",
			cfg: &config.Config{
				OpenAICompatibility: []config.OpenAICompatibility{
					{
						Name:    "test-provider",
						WireAPI: "responses",
					},
				},
			},
			auth: &cliproxyauth.Auth{
				Provider: "test-provider",
			},
			expected: "responses",
		},
		{
			name: "config with chat wire-api",
			cfg: &config.Config{
				OpenAICompatibility: []config.OpenAICompatibility{
					{
						Name:    "test-provider",
						WireAPI: "chat",
					},
				},
			},
			auth: &cliproxyauth.Auth{
				Provider: "test-provider",
			},
			expected: "chat",
		},
		{
			name: "config with empty wire-api defaults to chat",
			cfg: &config.Config{
				OpenAICompatibility: []config.OpenAICompatibility{
					{
						Name:    "test-provider",
						WireAPI: "",
					},
				},
			},
			auth: &cliproxyauth.Auth{
				Provider: "test-provider",
			},
			expected: "chat",
		},
		{
			name: "provider not found defaults to chat",
			cfg: &config.Config{
				OpenAICompatibility: []config.OpenAICompatibility{
					{
						Name:    "other-provider",
						WireAPI: "responses",
					},
				},
			},
			auth: &cliproxyauth.Auth{
				Provider: "test-provider",
			},
			expected: "chat",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &OpenAICompatExecutor{cfg: tt.cfg}
			got := e.resolveWireAPI(tt.auth)
			if got != tt.expected {
				t.Errorf("resolveWireAPI() = %q, want %q", got, tt.expected)
			}
		})
	}
}
