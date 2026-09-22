package helps

import (
	"testing"
)

func TestHasMeaningfulCodexOutputDelta(t *testing.T) {
	// Empty string deltas must NOT be treated as meaningful output
	if HasMeaningfulCodexOutputDelta([]byte(`{"type":"response.output_text.delta","delta":""}`)) {
		t.Fatal("empty output_text.delta must return false")
	}
	if HasMeaningfulCodexOutputDelta([]byte(`{"type":"response.output_text.delta","delta":"   "}`)) {
		t.Fatal("whitespace output_text.delta must return false")
	}
	if HasMeaningfulCodexOutputDelta([]byte(`{"type":"response.reasoning_text.delta","delta":""}`)) {
		t.Fatal("empty reasoning_text.delta must return false")
	}
	if HasMeaningfulCodexOutputDelta([]byte(`{"type":"response.function_call_arguments.delta","delta":""}`)) {
		t.Fatal("empty function_call_arguments.delta must return false")
	}

	// Meaningful content deltas
	if !HasMeaningfulCodexOutputDelta([]byte(`{"type":"response.output_text.delta","delta":"Hello"}`)) {
		t.Fatal("non-empty output_text.delta must return true")
	}
	if !HasMeaningfulCodexOutputDelta([]byte(`{"type":"response.reasoning_text.delta","delta":"thinking"}`)) {
		t.Fatal("non-empty reasoning_text.delta must return true")
	}
	if !HasMeaningfulCodexOutputDelta([]byte(`{"type":"response.function_call_arguments.delta","delta":"{\"id\":1}"}`)) {
		t.Fatal("non-empty function_call_arguments.delta must return true")
	}
}

func TestIsCodexTerminalEmptyIncomplete(t *testing.T) {
	// Case 1: True empty incomplete with explicit numeric 0
	trueEmpty := []byte(`{"type":"response.incomplete","response":{"id":"r1","output":[],"usage":{"output_tokens":0}}}`)
	if !IsCodexTerminalEmptyIncomplete(trueEmpty, 0, false) {
		t.Fatal("expected true for true empty incomplete with numeric 0 tokens")
	}

	// Case 2: Partial output with non-empty text deltas emitted
	if IsCodexTerminalEmptyIncomplete(trueEmpty, 0, true) {
		t.Fatal("expected false when output deltas were observed")
	}

	// Case 3: Partial output with completed output items
	if IsCodexTerminalEmptyIncomplete(trueEmpty, 1, false) {
		t.Fatal("expected false when output items exist")
	}

	// Case 4: Positive output tokens
	positiveTokens := []byte(`{"type":"response.incomplete","response":{"id":"r1","output":[],"usage":{"output_tokens":5}}}`)
	if IsCodexTerminalEmptyIncomplete(positiveTokens, 0, false) {
		t.Fatal("expected false when output_tokens > 0")
	}

	// Case 5: Float output tokens (e.g. 0.5) must NOT be truncated to 0
	floatTokens := []byte(`{"type":"response.incomplete","response":{"id":"r1","output":[],"usage":{"output_tokens":0.5}}}`)
	if IsCodexTerminalEmptyIncomplete(floatTokens, 0, false) {
		t.Fatal("expected false when output_tokens is 0.5")
	}

	// Case 6: Missing output_tokens field
	missingTokens := []byte(`{"type":"response.incomplete","response":{"id":"r1","output":[],"usage":{}}}`)
	if IsCodexTerminalEmptyIncomplete(missingTokens, 0, false) {
		t.Fatal("expected false when output_tokens is missing")
	}

	// Case 7: Null output_tokens field
	nullTokens := []byte(`{"type":"response.incomplete","response":{"id":"r1","output":[],"usage":{"output_tokens":null}}}`)
	if IsCodexTerminalEmptyIncomplete(nullTokens, 0, false) {
		t.Fatal("expected false when output_tokens is null")
	}

	// Case 8: response.output array contains items
	nonEmptyOutput := []byte(`{"type":"response.incomplete","response":{"id":"r1","output":[{"type":"message"}],"usage":{"output_tokens":0}}}`)
	if IsCodexTerminalEmptyIncomplete(nonEmptyOutput, 0, false) {
		t.Fatal("expected false when response.output is non-empty")
	}

	// Case 9: Different event type (response.completed)
	completedEvent := []byte(`{"type":"response.completed","response":{"id":"r1","output":[],"usage":{"output_tokens":0}}}`)
	if IsCodexTerminalEmptyIncomplete(completedEvent, 0, false) {
		t.Fatal("expected false for response.completed")
	}
}
