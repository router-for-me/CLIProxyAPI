package executor

import (
	"net/http"
	"strings"
	"testing"
)

func TestAntigravityErrorMessage_AddsLicenseHintForKnown403(t *testing.T) {
	body := []byte(`{"error":{"code":403,"message":"SUBSCRIPTION_REQUIRED: Gemini Code Assist license missing","status":"PERMISSION_DENIED"}}`)
	msg := antigravityErrorMessage(http.StatusForbidden, body)
	if !strings.Contains(msg, "Hint:") {
		t.Fatalf("expected hint in message, got %q", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "gemini code assist license") {
		t.Fatalf("expected license text in message, got %q", msg)
	}
}

func TestAntigravityErrorMessage_NoHintForNon403(t *testing.T) {
	body := []byte(`{"error":"bad request"}`)
	msg := antigravityErrorMessage(http.StatusBadRequest, body)
	if strings.Contains(msg, "Hint:") {
		t.Fatalf("did not expect hint for non-403, got %q", msg)
	}
}
