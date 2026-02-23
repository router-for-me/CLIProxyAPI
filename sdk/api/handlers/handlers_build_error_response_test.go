package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestBuildErrorResponseBody_PreservesOpenAIEnvelopeJSON(t *testing.T) {
	raw := `{"error":{"message":"bad upstream","type":"invalid_request_error","code":"model_not_found"}}`
	body := BuildErrorResponseBody(http.StatusNotFound, raw)
	if string(body) != raw {
		t.Fatalf("expected raw JSON passthrough, got %s", string(body))
	}
}

func TestBuildErrorResponseBody_RewrapsJSONWithoutErrorField(t *testing.T) {
	body := BuildErrorResponseBody(http.StatusBadRequest, `{"message":"oops"}`)

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("expected valid JSON, got error: %v", err)
	}
	errObj, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected top-level error envelope, got %s", string(body))
	}
	msg, _ := errObj["message"].(string)
	if !strings.Contains(msg, "without top-level error field") {
		t.Fatalf("unexpected message %q", msg)
	}
}

func TestBuildErrorResponseBody_NotFoundAddsModelHint(t *testing.T) {
	body := BuildErrorResponseBody(http.StatusNotFound, "The requested model 'gpt-5.3-codex' does not exist.")

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("expected valid JSON, got error: %v", err)
	}
	errObj, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected top-level error envelope, got %s", string(body))
	}
	msg, _ := errObj["message"].(string)
	if !strings.Contains(msg, "GET /v1/models") {
		t.Fatalf("expected model discovery hint in %q", msg)
	}
	code, _ := errObj["code"].(string)
	if code != "model_not_found" {
		t.Fatalf("expected model_not_found code, got %q", code)
	}
}
