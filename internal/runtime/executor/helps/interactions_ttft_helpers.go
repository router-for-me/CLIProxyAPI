package helps

import (
	"bytes"

	"github.com/MoeForge/moego/ojson"
)

// IsInteractionsTokenEvent reports whether an Interactions API streaming event carries output:
// a text, thought summary or tool argument delta. Terminal events count too, so TTFT is still
// recorded on a turn that ends without a delta. Lifecycle events such as interaction.created,
// step.start and step.stop do not count.
func IsInteractionsTokenEvent(payload []byte) bool {
	payload = bytes.TrimSpace(payload)
	if bytes.HasPrefix(payload, []byte("data:")) {
		payload = bytes.TrimSpace(payload[len("data:"):])
	}
	if len(payload) == 0 {
		return false
	}

	switch ojson.GetBytesNoCopy(payload, "event_type").String() {
	case "step.delta":
		delta := ojson.GetBytesNoCopy(payload, "delta")
		switch delta.Get("type").String() {
		case "text", "thought_summary":
			return len(delta.Get("text").String()) > 0 || len(delta.Get("content.text").String()) > 0
		case "arguments_delta":
			return len(delta.Get("arguments").String()) > 0
		}
		return false
	case "interaction.completed", "interaction.failed", "response.failed", "finish", "error":
		return true
	default:
		return false
	}
}

// ObserveInteractionsTokenEvent records TTFT on the reporter when payload is the first
// Interactions event that carries output.
func ObserveInteractionsTokenEvent(reporter *UsageReporter, payload []byte) {
	if reporter == nil || len(payload) == 0 {
		return
	}
	reporter.ObserveResponseModel(payload)
	if reporter.IsTTFTSet() {
		return
	}
	reporter.ObserveTokenEvent(IsInteractionsTokenEvent(payload))
}
