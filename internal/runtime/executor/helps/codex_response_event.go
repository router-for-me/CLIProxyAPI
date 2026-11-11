package helps

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// CodexResponseEventType recognizes error envelopes that omit the event type.
// Explicit event types remain authoritative so output and metadata cannot be
// mistaken for a terminal failure merely because they contain an error field.
func CodexResponseEventType(payload []byte) string {
	if eventType := strings.TrimSpace(ojson.GetBytesNoCopy(payload, "type").String()); eventType != "" {
		return eventType
	}
	if !ojson.ValidBytes(payload) {
		return ""
	}
	for _, path := range []string{"error", "body.error", "response.error"} {
		node := ojson.GetBytesNoCopy(payload, path)
		if node.IsObject() || node.Type == ojson.String && strings.TrimSpace(node.String()) != "" {
			return "error"
		}
	}
	if strings.TrimSpace(ojson.GetBytesNoCopy(payload, "code").String()) != "" &&
		strings.TrimSpace(ojson.GetBytesNoCopy(payload, "message").String()) != "" {
		return "error"
	}
	return ""
}
