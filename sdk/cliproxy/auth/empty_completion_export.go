package auth

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

// Finish flushes any trailing pending fragment at EOF and reports whether the
// accumulated stream chunks represent a terminal empty completion.
func (d *StreamBootstrapDetector) Finish() bool {
	if d == nil {
		return false
	}
	d.state.finish()
	return d.state.isEmptyCompletion()
}
