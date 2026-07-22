package openai

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestSanitizeResponsesWebsocketHTTPPayloadRemovesGenerate(t *testing.T) {
	payload := []byte(`{"type":"response.create","model":"gpt-5-codex","generate":false,"input":[]}`)
	sanitized := sanitizeResponsesWebsocketHTTPPayload(payload)
	if gjson.GetBytes(sanitized, "generate").Exists() {
		t.Fatalf("generate leaked into HTTP replay payload: %s", sanitized)
	}
	if !gjson.GetBytes(sanitized, "input").Exists() {
		t.Fatalf("sanitizer removed unrelated input field: %s", sanitized)
	}
}
