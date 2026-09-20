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
