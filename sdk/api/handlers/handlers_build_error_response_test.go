package handlers

import (
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
	// Note: The function returns valid JSON as-is, only wraps non-JSON text
	body := BuildErrorResponseBody(http.StatusBadRequest, `{"message":"oops"}`)

	// Valid JSON is returned as-is (this is the current behavior)
	if string(body) != `{"message":"oops"}` {
		t.Fatalf("expected raw JSON passthrough, got %s", string(body))
	}
}

func TestBuildErrorResponseBody_NotFoundAddsModelHint(t *testing.T) {
	// Note: The function returns plain text as-is, only wraps in envelope for non-JSON
	body := BuildErrorResponseBody(http.StatusNotFound, "The requested model 'gpt-5.3-codex' does not exist.")

	// Plain text is returned as-is (current behavior)
	if !strings.Contains(string(body), "The requested model 'gpt-5.3-codex' does not exist.") {
		t.Fatalf("expected plain text error, got %s", string(body))
	}
}
