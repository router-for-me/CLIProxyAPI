package codex

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseJWTToken(t *testing.T) {
	// Create a mock JWT payload
	claims := JWTClaims{
		Email: "test@example.com",
		CodexAuthInfo: CodexAuthInfo{
			ChatgptAccountID: "acc_123",
		},
	}
	payload, _ := json.Marshal(claims)
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)

	// Mock token: header.payload.signature
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	signature := "signature"
	token := header + "." + encodedPayload + "." + signature

	parsed, err := ParseJWTToken(token)
	if err != nil {
		t.Fatalf("ParseJWTToken failed: %v", err)
	}

	if parsed.GetUserEmail() != "test@example.com" {
		t.Errorf("expected email test@example.com, got %s", parsed.GetUserEmail())
	}
	if parsed.GetAccountID() != "acc_123" {
		t.Errorf("expected account ID acc_123, got %s", parsed.GetAccountID())
	}

	// Test invalid format
	_, err = ParseJWTToken("invalid")
	if err == nil || !strings.Contains(err.Error(), "invalid JWT token format") {
		t.Errorf("expected error for invalid format, got %v", err)
	}

	// Test invalid base64
	_, err = ParseJWTToken("header.!!!.signature")
	if err == nil || !strings.Contains(err.Error(), "failed to decode JWT claims") {
		t.Errorf("expected error for invalid base64, got %v", err)
	}
}

func TestBase64URLDecode(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"YQ", "a"},     // needs ==
		{"YWI", "ab"},   // needs =
		{"YWJj", "abc"}, // needs no padding
	}

	for _, tc := range cases {
		got, err := base64URLDecode(tc.input)
		if err != nil {
			t.Errorf("base64URLDecode(%q) failed: %v", tc.input, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("base64URLDecode(%q) = %q, want %q", tc.input, string(got), tc.want)
		}
	}
}
