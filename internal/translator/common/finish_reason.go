// Package common provides shared translator helpers.
package common

// ClaudeStopReasonToOpenAIFinishReason maps Claude stop_reason to OpenAI
// chat-completions finish_reason. All unmapped values fall back to "stop".
func ClaudeStopReasonToOpenAIFinishReason(reason string) string {
	switch reason {
	case "end_turn":
		return "stop"
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	default:
		return "stop"
	}
}

// OpenAIFinishReasonToClaudeStopReason maps OpenAI finish_reason to Claude
// stop_reason. When hasToolCall is true and reason is "stop", returns "tool_use".
func OpenAIFinishReasonToClaudeStopReason(reason string, hasToolCall bool) string {
	if hasToolCall && reason == "stop" {
		return "tool_use"
	}
	switch reason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "refusal"
	case "function_call": // Legacy OpenAI
		return "tool_use"
	default:
		return "end_turn"
	}
}

// OpenAIFinishReasonToGeminiFinishReason maps OpenAI finish_reason to Gemini
// finish reason enum values. All unmapped values fall back to "STOP".
func OpenAIFinishReasonToGeminiFinishReason(reason string) string {
	switch reason {
	case "stop":
		return "STOP"
	case "length":
		return "MAX_TOKENS"
	case "tool_calls":
		return "STOP" // Gemini has no tool_calls finish reason
	case "content_filter":
		return "SAFETY"
	default:
		return "STOP"
	}
}

// ClaudeStopReasonToGeminiFinishReason maps Claude stop_reason to Gemini
// finish reason enum values. All unmapped values fall back to "STOP".
func ClaudeStopReasonToGeminiFinishReason(reason string) string {
	switch reason {
	case "end_turn", "tool_use", "stop_sequence":
		return "STOP"
	case "max_tokens":
		return "MAX_TOKENS"
	default:
		return "STOP"
	}
}

// CodexStopReasonToClaudeStopReason maps Codex stop_reason to Claude
// stop_reason. When hasToolCall is true, always returns "tool_use".
func CodexStopReasonToClaudeStopReason(stopReason string, hasToolCall bool) string {
	if hasToolCall {
		return "tool_use"
	}
	switch stopReason {
	case "", "stop", "completed":
		return "end_turn"
	case "max_tokens", "max_output_tokens":
		return "max_tokens"
	case "tool_use", "tool_calls", "function_call":
		return "end_turn"
	case "end_turn", "stop_sequence", "pause_turn", "refusal", "model_context_window_exceeded":
		return stopReason
	case "content_filter":
		return "refusal"
	default:
		return "end_turn"
	}
}
