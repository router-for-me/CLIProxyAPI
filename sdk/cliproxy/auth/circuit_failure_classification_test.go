package auth

import (
	"strings"
	"testing"
)

func TestNormalizeCircuitBreakerModelID_RemovesThinkingSuffix(t *testing.T) {
	got := NormalizeCircuitBreakerModelID(" gpt-5(high) ")
	if got != "gpt-5" {
		t.Fatalf("NormalizeCircuitBreakerModelID() = %q, want %q", got, "gpt-5")
	}
}

func TestIsCircuitCountableFailure_InvalidRequestSkipped(t *testing.T) {
	countable, reason := IsCircuitCountableFailure(400, "invalid_request_error: malformed payload")
	if countable {
		t.Fatal("countable = true, want false")
	}
	if reason != "invalid_request" {
		t.Fatalf("skip reason = %q, want %q", reason, "invalid_request")
	}
}

func TestIsCircuitCountableFailure_ModelUnsupportedStillCountable(t *testing.T) {
	countable, reason := IsCircuitCountableFailure(400, "invalid_request_error: requested model is not supported")
	if !countable {
		t.Fatalf("countable = false, want true (reason=%q)", reason)
	}
	if reason != "" {
		t.Fatalf("skip reason = %q, want empty", reason)
	}
}

func TestSanitizeErrorMessageForStore_MasksSecretsAndTruncates(t *testing.T) {
	raw := "authorization: bearer super-secret-token token=abcd1234 " + strings.Repeat("x", 1200)
	masked, hash := SanitizeErrorMessageForStore(raw, 64)
	if hash == "" {
		t.Fatal("hash should not be empty")
	}
	if strings.Contains(masked, "super-secret-token") || strings.Contains(masked, "abcd1234") {
		t.Fatalf("masked message leaked secret: %q", masked)
	}
	if len([]rune(masked)) != 64 {
		t.Fatalf("masked rune length = %d, want 64", len([]rune(masked)))
	}
}
