package auth

import "testing"

func TestIsConfigAPIKeyAuth(t *testing.T) {
	if IsConfigAPIKeyAuth(nil) {
		t.Fatal("expected nil auth to be false")
	}
	if IsConfigAPIKeyAuth(&Auth{Attributes: map[string]string{"source": "config:codex[x]"}}) {
		t.Fatal("expected missing api_key to be false")
	}
	if IsConfigAPIKeyAuth(&Auth{
		ID:       "codex:oauth:abc",
		Provider: "codex",
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"api_key":   "k",
			"source":    "config:codex[abc]",
		},
	}) {
		t.Fatal("expected explicit oauth auth to be false")
	}
	if !IsConfigAPIKeyAuth(&Auth{
		ID:       "codex:apikey:abc",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key": "k",
			"source":  "config:codex[abc]",
		},
	}) {
		t.Fatal("expected config api key auth")
	}
}
