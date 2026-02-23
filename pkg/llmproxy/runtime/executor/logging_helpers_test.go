package executor

import (
	"strings"
	"testing"
)

func TestExtractJSONErrorMessage_ModelNotFoundAddsGuidance(t *testing.T) {
	body := []byte(`{"error":{"code":"model_not_found","message":"model not found: foo"}}`)
	got := extractJSONErrorMessage(body)
	if !strings.Contains(got, "GET /v1/models") {
		t.Fatalf("expected /v1/models guidance, got %q", got)
	}
}

func TestExtractJSONErrorMessage_CodexModelAddsResponsesHint(t *testing.T) {
	body := []byte(`{"error":{"message":"model not found for gpt-5.3-codex"}}`)
	got := extractJSONErrorMessage(body)
	if !strings.Contains(got, "/v1/responses") {
		t.Fatalf("expected /v1/responses hint, got %q", got)
	}
}

func TestExtractJSONErrorMessage_NonModelErrorUnchanged(t *testing.T) {
	body := []byte(`{"error":{"message":"rate limit exceeded"}}`)
	got := extractJSONErrorMessage(body)
	if got != "rate limit exceeded" {
		t.Fatalf("expected unchanged message, got %q", got)
	}
}

func TestExtractJSONErrorMessage_ExistingGuidanceNotDuplicated(t *testing.T) {
	body := []byte(`{"error":{"message":"model not found; check /v1/models"}}`)
	got := extractJSONErrorMessage(body)
	if got != "model not found; check /v1/models" {
		t.Fatalf("expected existing guidance to remain unchanged, got %q", got)
	}
}
