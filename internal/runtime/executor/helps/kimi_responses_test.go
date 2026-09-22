package helps

import (
	"testing"

	kimiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kimi"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestResolveKimiResponsesURL(t *testing.T) {
	tests := []struct {
		name string
		auth *cliproxyauth.Auth
		want string
	}{
		{
			name: "nil auth",
			auth: nil,
			want: kimiauth.KimiAPIBaseURL + "/v1/responses",
		},
		{
			name: "empty attributes",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{}},
			want: kimiauth.KimiAPIBaseURL + "/v1/responses",
		},
		{
			name: "base_url without v1",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.com/coding"}},
			want: "https://api.kimi.com/coding/v1/responses",
		},
		{
			name: "base_url with trailing slash",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.com/coding/"}},
			want: "https://api.kimi.com/coding/v1/responses",
		},
		{
			name: "base_url with v1",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.com/coding/v1"}},
			want: "https://api.kimi.com/coding/v1/responses",
		},
		{
			name: "base_url with v1 and trailing slash",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.com/coding/v1/"}},
			want: "https://api.kimi.com/coding/v1/responses",
		},
		{
			name: "kimi.ai auth by provider",
			auth: &cliproxyauth.Auth{Provider: "kimi-ai"},
			want: kimiauth.KimiAIAPIBaseURL + "/v1/responses",
		},
		{
			name: "kimi.ai auth by domain attribute",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"domain": "kimi.ai"}},
			want: kimiauth.KimiAIAPIBaseURL + "/v1/responses",
		},
		{
			name: "kimi.ai base_url in attributes",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.ai/coding"}},
			want: "https://api.kimi.ai/coding/v1/responses",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveKimiResponsesURL(tt.auth)
			if got != tt.want {
				t.Fatalf("ResolveKimiResponsesURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveKimiChatURL(t *testing.T) {
	tests := []struct {
		name string
		auth *cliproxyauth.Auth
		want string
	}{
		{
			name: "nil auth",
			auth: nil,
			want: kimiauth.KimiAPIBaseURL + "/v1/chat/completions",
		},
		{
			name: "base_url without v1",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.ai/coding"}},
			want: "https://api.kimi.ai/coding/v1/chat/completions",
		},
		{
			name: "base_url with trailing v1",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.ai/coding/v1"}},
			want: "https://api.kimi.ai/coding/v1/chat/completions",
		},
		{
			name: "base_url with trailing slash after v1",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.ai/coding/v1/"}},
			want: "https://api.kimi.ai/coding/v1/chat/completions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveKimiChatURL(tt.auth)
			if got != tt.want {
				t.Fatalf("ResolveKimiChatURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveKimiClaudeBaseURL(t *testing.T) {
	tests := []struct {
		name string
		auth *cliproxyauth.Auth
		want string
	}{
		{
			name: "nil auth",
			auth: nil,
			want: kimiauth.KimiAPIBaseURL,
		},
		{
			name: "base_url with v1",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.ai/coding/v1"}},
			want: "https://api.kimi.ai/coding",
		},
		{
			name: "base_url without v1",
			auth: &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://api.kimi.ai/coding"}},
			want: "https://api.kimi.ai/coding",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveKimiClaudeBaseURL(tt.auth)
			if got != tt.want {
				t.Fatalf("ResolveKimiClaudeBaseURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
