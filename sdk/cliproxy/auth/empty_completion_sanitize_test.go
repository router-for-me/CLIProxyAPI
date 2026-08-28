package auth

import (
	"strings"
	"testing"
)

// TestParseStreamErrorSanitizesCodeAndMessage verifies that evalProviderError
// (which calls parseStreamErrorFromEnvelope) redacts credentials from both the
// parsed code and message, so the secret never reaches logs, results, or
// LastError.
func TestParseStreamErrorSanitizesCodeAndMessage(t *testing.T) {
	const codeSecret = "sk-live-test12345"
	const msgSecret = "sk-abc123def456"

	data := []byte(`{"error":{"code":"` + codeSecret + `","message":"Bearer ` + msgSecret + `"}}`)
	got := evalProviderError(data, "error")
	if got == nil {
		t.Fatal("evalProviderError returned nil for an error payload")
	}
	if strings.Contains(got.Code, codeSecret) {
		t.Fatalf("parsed error code leaks code secret: %q", got.Code)
	}
	if !strings.Contains(got.Code, "REDACTED") {
		t.Fatalf("parsed error code did not redact code secret: %q", got.Code)
	}
	if strings.Contains(got.Message, msgSecret) {
		t.Fatalf("parsed error message leaks message secret: %q", got.Message)
	}
	if !strings.Contains(got.Message, "REDACTED") {
		t.Fatalf("parsed error message did not redact message secret: %q", got.Message)
	}

	// The log summary path must not resurface the raw secret either.
	summary := summarizeErrorForLog(got)
	if strings.Contains(summary, codeSecret) || strings.Contains(summary, msgSecret) {
		t.Fatalf("log summary leaks parsed secret: %q", summary)
	}

	// The recorded result path must not resurface the raw secret either.
	result := resultErrorFromError(got)
	if strings.Contains(result.Code, codeSecret) || strings.Contains(result.Message, msgSecret) {
		t.Fatalf("recorded result leaks parsed secret: code=%q message=%q", result.Code, result.Message)
	}
}
