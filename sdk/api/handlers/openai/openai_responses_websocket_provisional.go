package openai

import (
	"strings"
)

// isResponsesWebsocketMeaningfulEvent reports whether forwarding an event has
// exposed model output that cannot safely be replayed to the downstream client.
// Lifecycle and tool-item metadata remain buffered so a replayable upstream
// state-loss error can be intercepted before it is exposed downstream.
func isResponsesWebsocketMeaningfulEvent(eventType string) bool {
	if strings.HasSuffix(strings.TrimSpace(eventType), ".delta") {
		return true
	}
	return isResponsesWebsocketCompletionEvent(eventType)
}
