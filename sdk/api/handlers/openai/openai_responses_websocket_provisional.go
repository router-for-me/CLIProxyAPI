package openai

import "strings"

func isResponsesWebsocketProvisionalEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "response.created", "response.in_progress", "response.queued":
		return true
	default:
		return false
	}
}
