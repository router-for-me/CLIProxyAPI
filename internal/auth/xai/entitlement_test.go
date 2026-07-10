package xai

import (
	"encoding/base64"
	"testing"
)

func TestAccessTokenHasStandardAPITier(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{name: "missing tier", payload: `{"scope":"api:access"}`, want: false},
		{name: "positive numeric tier", payload: `{"tier":4}`, want: true},
		{name: "zero numeric tier", payload: `{"tier":0}`, want: false},
		{name: "positive string tier", payload: `{"tier":"4"}`, want: true},
		{name: "unknown named tier", payload: `{"tier":"pro"}`, want: false},
		{name: "named free tier", payload: `{"tier":"free"}`, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := "header." + base64.RawURLEncoding.EncodeToString([]byte(tt.payload)) + ".signature"
			if got := AccessTokenHasStandardAPITier(token); got != tt.want {
				t.Fatalf("AccessTokenHasStandardAPITier() = %v, want %v", got, tt.want)
			}
		})
	}

	for _, token := range []string{"", "opaque-token", "header.%%%invalid.signature"} {
		if AccessTokenHasStandardAPITier(token) {
			t.Fatalf("AccessTokenHasStandardAPITier(%q) = true, want false", token)
		}
	}
}

func TestOAuthModelUsesGrokCLI(t *testing.T) {
	paidToken := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"tier":4}`)) + ".signature"
	tests := []struct {
		name     string
		authKind string
		token    string
		model    string
		want     bool
	}{
		{name: "free oauth", authKind: "oauth", token: "opaque-free-token", model: FreeOAuthModel, want: true},
		{name: "paid oauth", authKind: "oauth", token: paidToken, model: FreeOAuthModel, want: false},
		{name: "paid composer", authKind: "oauth", token: paidToken, model: "grok-composer-2.5-fast", want: true},
		{name: "api key", authKind: "apikey", token: "api-key", model: FreeOAuthModel, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OAuthModelUsesGrokCLI(tt.authKind, tt.token, tt.model); got != tt.want {
				t.Fatalf("OAuthModelUsesGrokCLI() = %v, want %v", got, tt.want)
			}
		})
	}
}
