package pluginhost

import (
	"context"
	"errors"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// Regression tests for codex pullrequestreview-4943660625 on PR #4881
// (pluginhost stream wrapper EOF handling).

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

// TestWrapStreamEmptyCompletionMultiChoiceWithholdsTerminalUntilAllChoicesFinish
// verifies that when n=2 is requested, an early terminal chunk for choice 0
// does not cause an immediate empty_completion error if choice 1 subsequently emits content.
func TestWrapStreamEmptyCompletionMultiChoiceForwardsContentWhenChoice1HasContent(t *testing.T) {
	src := make(chan coreexecutor.StreamChunk, 3)
	src <- coreexecutor.StreamChunk{Payload: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")}
	src <- coreexecutor.StreamChunk{Payload: []byte("data: {\"choices\":[{\"index\":1,\"delta\":{\"content\":\"hello\"}}]}\n\n")}
	src <- coreexecutor.StreamChunk{Payload: []byte("data: [DONE]\n\n")}
	close(src)

	reqPayload := []byte(`{"model":"gpt-4o","n":2,"messages":[{"role":"user","content":"hi"}]}`)
	wrapped := wrapStreamEmptyCompletion(context.Background(), &coreexecutor.StreamResult{Chunks: src}, reqPayload)

	first, ok := <-wrapped.Chunks
	if !ok {
		t.Fatal("wrapped stream closed prematurely")
	}
	if first.Err != nil {
		t.Fatalf("unexpected error on first chunk: %v", first.Err)
	}
	second, ok := <-wrapped.Chunks
	if !ok {
		t.Fatal("wrapped stream missing second chunk")
	}
	if string(second.Payload) != "data: {\"choices\":[{\"index\":1,\"delta\":{\"content\":\"hello\"}}]}\n\n" {
		t.Fatalf("second payload = %q, want choice 1 content", second.Payload)
	}
}

// TestWrapStreamEmptyCompletionMultiChoiceDetectsEmptyWhenAllChoicesFinishEmpty
// verifies that when n=2 is requested and both choices finish empty,
// wrapStreamEmptyCompletion correctly detects empty completion.
func TestWrapStreamEmptyCompletionMultiChoiceDetectsEmptyWhenAllChoicesFinishEmpty(t *testing.T) {
	src := make(chan coreexecutor.StreamChunk, 2)
	src <- coreexecutor.StreamChunk{Payload: []byte("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")}
	src <- coreexecutor.StreamChunk{Payload: []byte("data: {\"choices\":[{\"index\":1,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")}
	close(src)

	reqPayload := []byte(`{"model":"gpt-4o","n":2,"messages":[{"role":"user","content":"hi"}]}`)
	wrapped := wrapStreamEmptyCompletion(context.Background(), &coreexecutor.StreamResult{Chunks: src}, reqPayload)

	first, ok := <-wrapped.Chunks
	if !ok {
		t.Fatal("wrapped multi-choice empty stream closed without empty_completion error")
	}
	var authErr *coreauth.Error
	if !errors.As(first.Err, &authErr) || authErr.Code != "empty_completion" {
		t.Fatalf("first error = %v, want empty_completion", first.Err)
	}
}

// TestWrapStreamEmptyCompletionGeminiMultiCandidateForwardsContentWhenCandidate1HasContent
// verifies that when generationConfig.candidateCount=2 is requested, an early STOP chunk for candidate 0
// does not cause an immediate empty_completion error if candidate 1 subsequently emits content.
func TestWrapStreamEmptyCompletionGeminiMultiCandidateForwardsContentWhenCandidate1HasContent(t *testing.T) {
	src := make(chan coreexecutor.StreamChunk, 2)
	src <- coreexecutor.StreamChunk{Payload: []byte("data: {\"candidates\":[{\"index\":0,\"content\":{\"parts\":[]},\"finishReason\":\"STOP\"}]}\n\n")}
	src <- coreexecutor.StreamChunk{Payload: []byte("data: {\"candidates\":[{\"index\":1,\"content\":{\"parts\":[{\"text\":\"gemini answer\"}]}}]}\n\n")}
	close(src)

	reqPayload := []byte(`{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"candidateCount":2}}`)
	wrapped := wrapStreamEmptyCompletion(context.Background(), &coreexecutor.StreamResult{Chunks: src}, reqPayload)

	first, ok := <-wrapped.Chunks
	if !ok {
		t.Fatal("wrapped stream closed prematurely")
	}
	if first.Err != nil {
		t.Fatalf("unexpected error on first chunk: %v", first.Err)
	}
	second, ok := <-wrapped.Chunks
	if !ok {
		t.Fatal("wrapped stream missing second chunk")
	}
	if string(second.Payload) != "data: {\"candidates\":[{\"index\":1,\"content\":{\"parts\":[{\"text\":\"gemini answer\"}]}}]}\n\n" {
		t.Fatalf("second payload = %q, want candidate 1 content", second.Payload)
	}
}

// TestWrapStreamEmptyCompletionGeminiMultiCandidateDetectsEmptyWhenAllCandidatesFinishEmpty
// verifies that when candidateCount=2 is requested and both candidates finish empty,
// wrapStreamEmptyCompletion correctly detects empty completion.
func TestWrapStreamEmptyCompletionGeminiMultiCandidateDetectsEmptyWhenAllCandidatesFinishEmpty(t *testing.T) {
	src := make(chan coreexecutor.StreamChunk, 2)
	src <- coreexecutor.StreamChunk{Payload: []byte("data: {\"candidates\":[{\"index\":0,\"content\":{\"parts\":[]},\"finishReason\":\"STOP\"}]}\n\n")}
	src <- coreexecutor.StreamChunk{Payload: []byte("data: {\"candidates\":[{\"index\":1,\"content\":{\"parts\":[]},\"finishReason\":\"STOP\"}]}\n\n")}
	close(src)

	reqPayload := []byte(`{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"candidateCount":2}}`)
	wrapped := wrapStreamEmptyCompletion(context.Background(), &coreexecutor.StreamResult{Chunks: src}, reqPayload)

	first, ok := <-wrapped.Chunks
	if !ok {
		t.Fatal("wrapped multi-candidate empty stream closed without empty_completion error")
	}
	var authErr *coreauth.Error
	if !errors.As(first.Err, &authErr) || authErr.Code != "empty_completion" {
		t.Fatalf("first error = %v, want empty_completion", first.Err)
	}
}
