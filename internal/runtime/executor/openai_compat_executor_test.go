package executor

import (
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestBuildRequestURL(t *testing.T) {
	executor := &OpenAICompatExecutor{provider: "test"}

	tests := []struct {
		name     string
		baseURL  string
		auth     *cliproxyauth.Auth
		expected string
	}{
		{
			name:     "chat API default",
			baseURL:  "https://api.example.com/openai",
			auth:     nil,
			expected: "https://api.example.com/openai/chat/completions",
		},
		{
			name:    "chat API explicit",
			baseURL: "https://api.example.com/openai",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{"wire_api": "chat"},
			},
			expected: "https://api.example.com/openai/chat/completions",
		},
		{
			name:    "responses API without v1 suffix",
			baseURL: "https://api.example.com/openai",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{"wire_api": "responses"},
			},
			expected: "https://api.example.com/openai/v1/responses",
		},
		{
			name:    "responses API with v1 suffix",
			baseURL: "https://api.example.com/openai/v1",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{"wire_api": "responses"},
			},
			expected: "https://api.example.com/openai/v1/responses",
		},
		{
			name:    "responses API with v1 and trailing slash",
			baseURL: "https://api.example.com/openai/v1/",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{"wire_api": "responses"},
			},
			expected: "https://api.example.com/openai/v1/responses",
		},
		{
			name:    "responses API Azure style URL",
			baseURL: "https://myresource.openai.azure.com/openai",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{"wire_api": "responses"},
			},
			expected: "https://myresource.openai.azure.com/openai/v1/responses",
		},
		{
			name:    "responses API Azure style URL with v1",
			baseURL: "https://myresource.openai.azure.com/openai/v1",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{"wire_api": "responses"},
			},
			expected: "https://myresource.openai.azure.com/openai/v1/responses",
		},
		{
			name:    "chat API with trailing slash",
			baseURL: "https://api.example.com/openai/",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{"wire_api": "chat"},
			},
			expected: "https://api.example.com/openai/chat/completions",
		},
		{
			name:    "responses API with query params",
			baseURL: "https://api.example.com/openai",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{
					"wire_api":          "responses",
					"query:api-version": "preview",
				},
			},
			expected: "https://api.example.com/openai/v1/responses?api-version=preview",
		},
		{
			name:    "responses API with v1 and query params",
			baseURL: "https://api.example.com/openai/v1",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{
					"wire_api":          "responses",
					"query:api-version": "2024-02-01",
				},
			},
			expected: "https://api.example.com/openai/v1/responses?api-version=2024-02-01",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := executor.buildRequestURL(tt.baseURL, tt.auth)
			if result != tt.expected {
				t.Errorf("buildRequestURL() = %q, want %q", result, tt.expected)
			}
		})
	}
}
