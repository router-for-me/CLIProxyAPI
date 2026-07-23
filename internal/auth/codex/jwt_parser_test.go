package codex

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestParseJWTTokenAcceptsStringAudience(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"aud": "codex",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "account-123",
			"chatgpt_user_id":    "user-123",
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	token := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + ".signature"

	claims, err := ParseJWTToken(token)
	if err != nil {
		t.Fatalf("ParseJWTToken() error = %v", err)
	}
	if len(claims.Aud) != 1 || claims.Aud[0] != "codex" {
		t.Fatalf("Aud = %#v", claims.Aud)
	}
}

func TestParseJWTTokenRejectsMalformedCompactSerialization(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{}`))
	tests := []string{
		"." + payload + ".signature",
		"header..signature",
		"header." + payload + ".",
		"header." + payload + ".signature.extra",
	}
	for _, token := range tests {
		if _, err := ParseJWTToken(token); err == nil {
			t.Fatalf("ParseJWTToken(%q) unexpectedly succeeded", token)
		}
	}
}

func TestParseJWTTokenRejectsPaddedPayload(t *testing.T) {
	payload := base64.URLEncoding.EncodeToString([]byte(`{"sub":"padding"}`))
	if _, err := ParseJWTToken("header." + payload + ".signature"); err == nil {
		t.Fatal("ParseJWTToken() accepted padded JWT payload")
	}
}

func TestParseJWTTokenRejectsInvalidUTF8Payload(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte{'{', '"', 'e', 'm', 'a', 'i', 'l', '"', ':', '"', 0xff, '"', '}'})
	if _, err := ParseJWTToken("header." + payload + ".signature"); err == nil {
		t.Fatal("ParseJWTToken() accepted invalid UTF-8 payload")
	}
}
