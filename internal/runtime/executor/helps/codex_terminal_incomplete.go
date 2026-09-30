package helps

import (
	"strings"

	"github.com/tidwall/gjson"
)

const (
	// CodexEmptyIncompleteStreamMessage is the error message emitted when upstream ChatGPT Codex
	// terminates with response.incomplete having 0 output tokens and 0 content output.
	CodexEmptyIncompleteStreamMessage = "stream error: upstream terminated with incomplete empty response (0 tokens)"
)

// HasMeaningfulCodexOutputDelta reports whether an event carries non-empty generated content
// (such as non-empty text, reasoning, or function call arguments delta).
func HasMeaningfulCodexOutputDelta(eventData []byte) bool {
	eventType := gjson.GetBytes(eventData, "type").String()
	switch eventType {
	case "response.output_text.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
		delta := gjson.GetBytes(eventData, "delta")
		return delta.Exists() && len(strings.TrimSpace(delta.String())) > 0
	case "response.function_call_arguments.delta":
		delta := gjson.GetBytes(eventData, "delta")
		return delta.Exists() && len(strings.TrimSpace(delta.String())) > 0
	}
	return false
}

// IsCodexTerminalEmptyIncomplete reports whether a response.incomplete event represents
// an upstream silent abort where explicitly zero output tokens were generated and zero
// output content (no output items and no non-empty output deltas) was produced.
func IsCodexTerminalEmptyIncomplete(eventData []byte, outputItemsCount int, sawOutputDelta bool) bool {
	eventType := gjson.GetBytes(eventData, "type").String()
	if eventType != "response.incomplete" {
		return false
	}
	// If any non-empty text delta, reasoning delta, or tool argument delta was emitted, content was produced.
	if sawOutputDelta {
		return false
	}
	// If any completed output items exist, output was produced.
	if outputItemsCount > 0 {
		return false
	}
	// If response.output contains any items, output was produced.
	output := gjson.GetBytes(eventData, "response.output")
	if output.Exists() && output.IsArray() && len(output.Array()) > 0 {
		return false
	}
	// Require explicit numeric zero for output tokens (reject floats like 0.5, non-numbers, missing, or null).
	outputTokens := gjson.GetBytes(eventData, "response.usage.output_tokens")
	if !outputTokens.Exists() || outputTokens.Type != gjson.Number {
		return false
	}
	if outputTokens.Num != 0 || strings.TrimSpace(outputTokens.Raw) != "0" {
		return false
	}
	return true
}
