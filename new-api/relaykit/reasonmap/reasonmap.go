package reasonmap

import (
	"strings"

	"github.com/QuantumNous/new-api/relaykit/types"
)

func ClaudeStopReasonToOpenAIFinishReason(stopReason string) string {
	switch strings.ToLower(stopReason) {
	case "stop_sequence":
		return "stop"
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "pause_turn":
		// Responses has no pause_turn finish reason. Treat the provider's
		// resumable server-side loop as an incomplete response instead of a
		// successful stop; the hosted output items preserve continuation state.
		return "length"
	case "refusal":
		return types.FinishReasonContentFilter
	default:
		return stopReason
	}
}

func OpenAIFinishReasonToClaudeStopReason(finishReason string) string {
	switch strings.ToLower(finishReason) {
	case "stop":
		return "end_turn"
	case "stop_sequence":
		return "stop_sequence"
	case "length", "max_tokens":
		return "max_tokens"
	case types.FinishReasonContentFilter:
		return "refusal"
	case "tool_calls":
		return "tool_use"
	default:
		return finishReason
	}
}
