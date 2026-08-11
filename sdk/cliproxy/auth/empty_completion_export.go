package auth

import (
	"bytes"
)

// IsEmptyCompletionPayload reports whether a payload (aggregated SSE chunks or
// a single non-stream JSON response) represents a terminal but empty
// completion. It is the exported form of the internal predicate used by the
// conductor, exposed so the plugin-executor path can reject empty completions
// before they reach the client.
func IsEmptyCompletionPayload(payload []byte) bool {
	return isEmptyCompletionPayload(payload)
}

// EmptyCompletionError returns the retriable error used when upstream returns
// a terminal but empty completion. The plugin-executor path returns it so the
// client receives an error instead of a silent empty response, matching how
// the conductor surfaces empty completions.
func EmptyCompletionError() error {
	return errEmptyCompletion
}

// IsCompletionFormatRecognized reports whether payload uses a wire format the
// empty-completion detection understands (OpenAI chat, OpenAI Responses,
// Anthropic Claude, or Gemini). It supports representative format-contract
// tests without claiming registry-wide executor coverage.
func IsCompletionFormatRecognized(payload []byte) bool {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return false
	}
	var acc emptyCompletionAccum
	if bytes.Contains(trimmed, []byte("data:")) || bytes.HasPrefix(trimmed, []byte("event:")) {
		acc.evalSSE(trimmed)
	} else {
		acc.evalJSON(trimmed)
	}
	return acc.recognized
}

// StreamBootstrapDetector incrementally classifies a stream prefix without
// reparsing previously observed chunks. Its zero value is ready for use.
type StreamBootstrapDetector struct {
	state streamBootstrapState
}

// Observe records an arbitrary stream byte fragment and reports whether
// buffered data should now be forwarded. It retains incomplete SSE lines across
// calls and forwards conservatively at the bootstrap byte limit.
func (d *StreamBootstrapDetector) Observe(payload []byte) bool {
	if d == nil {
		return true
	}
	return d.state.observe(payload)
}
