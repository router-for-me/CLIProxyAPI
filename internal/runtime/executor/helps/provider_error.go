package helps

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// IsProviderErrorPayload is used only on upstream bytes, before translation.
// Locally generated error envelopes cannot establish a provider refusal.
func IsProviderErrorPayload(payload []byte) bool {
	return IsProviderErrorEventPayload("", payload)
}

// IsProviderErrorEventPayload accepts supported top-level details only within
// an explicit provider error event, never from a generated fallback envelope.
func IsProviderErrorEventPayload(eventName string, payload []byte) bool {
	if !ojson.ValidBytes(payload) {
		return false
	}
	for _, path := range []string{"error", "response.error", "body.error"} {
		err := ojson.GetBytesNoCopy(payload, path)
		if err.Type == ojson.String && strings.TrimSpace(err.String()) != "" {
			return true
		}
		if !err.IsObject() {
			continue
		}
		for _, field := range []string{"message", "type", "code", "status"} {
			if value := err.Get(field); value.Type == ojson.String && strings.TrimSpace(value.String()) != "" ||
				(field == "code" || field == "status") && value.Type == ojson.Number {
				return true
			}
		}
	}
	for _, event := range []string{eventName, ojson.GetBytesNoCopy(payload, "type").String()} {
		switch strings.ToLower(strings.TrimSpace(event)) {
		case "error", "response.error", "response.failed":
			for _, field := range []string{"message", "code", "error_type"} {
				value := ojson.GetBytesNoCopy(payload, field)
				if value.Type == ojson.String && strings.TrimSpace(value.String()) != "" || field == "code" && value.Type == ojson.Number {
					return true
				}
			}
		}
	}
	code := ojson.GetBytesNoCopy(payload, "code")
	message := ojson.GetBytesNoCopy(payload, "message")
	return (code.Type == ojson.Number || code.Type == ojson.String && strings.TrimSpace(code.String()) != "") &&
		message.Type == ojson.String && strings.TrimSpace(message.String()) != ""
}
