package pluginhost

import (
	"context"
	"errors"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// Regression tests mirrored from CLIProxyAPI PR #4881 follow-up
// (codex pullrequestreview-4943660625, pluginhost stream wrapper EOF
// handling).

// TestWrapStreamEmptyCompletionRejectsZeroPayloadChunkStream is a regression
// guard for the zero-payload finding: zero-payload chunks made the buffer
// non-empty, so the EOF branch skipped the empty_stream error and flushed a
// client-invisible stream as success.
func TestWrapStreamEmptyCompletionRejectsZeroPayloadChunkStream(t *testing.T) {
	src := make(chan coreexecutor.StreamChunk, 2)
	src <- coreexecutor.StreamChunk{Payload: nil}
	src <- coreexecutor.StreamChunk{Payload: []byte{}}
	close(src)

	wrapped := wrapStreamEmptyCompletion(context.Background(), &coreexecutor.StreamResult{Chunks: src})
	first, ok := <-wrapped.Chunks
	if !ok {
		t.Fatal("wrapped stream closed without empty_stream error")
	}
	var authErr *coreauth.Error
	if !errors.As(first.Err, &authErr) || authErr.Code != "empty_stream" || !authErr.Retryable {
		t.Fatalf("first error = %#v, want retryable empty_stream", first.Err)
	}
	if len(first.Payload) != 0 {
		t.Fatalf("first payload = %q, want error before client-visible bytes", first.Payload)
	}
	if _, ok = <-wrapped.Chunks; ok {
		t.Fatal("wrapped stream emitted chunks after empty_stream error")
	}
}

// TestWrapStreamEmptyCompletionDetectsSplitUsageOnlyStream is a regression
// guard for the detector.Finish finding: the EOF branch used to re-parse the
// concatenated payload, and separately chunked SSE frames without trailing
// newlines concatenated into invalid input, so the empty check failed and an
// empty plugin stream was flushed as success. The incremental detector state
// now decides at EOF.
func TestWrapStreamEmptyCompletionDetectsSplitUsageOnlyStream(t *testing.T) {
	src := make(chan coreexecutor.StreamChunk, 2)
	src <- coreexecutor.StreamChunk{Payload: []byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":0}}")}
	src <- coreexecutor.StreamChunk{Payload: []byte("data: [DONE]")}
	close(src)

	wrapped := wrapStreamEmptyCompletion(context.Background(), &coreexecutor.StreamResult{Chunks: src})
	first, ok := <-wrapped.Chunks
	if !ok {
		t.Fatal("wrapped split usage-only stream closed without empty_completion error")
	}
	var authErr *coreauth.Error
	if !errors.As(first.Err, &authErr) || authErr.Code != "empty_completion" {
		t.Fatalf("first error = %v, want empty_completion", first.Err)
	}
	if len(first.Payload) != 0 {
		t.Fatalf("first payload = %q, want no client-visible bytes before error", first.Payload)
	}
}
