package pluginhost

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestWrapStreamEmptyCompletionWithholdsTerminalFrames(t *testing.T) {
	src := make(chan coreexecutor.StreamChunk, 2)
	src <- coreexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"response.completed\",\"response\":{\"output\":[],\"usage\":{\"output_tokens\":0}}}\n\n")}
	src <- coreexecutor.StreamChunk{Payload: []byte("data: [DONE]\n\n")}
	close(src)

	wrapped := wrapStreamEmptyCompletion(context.Background(), &coreexecutor.StreamResult{Chunks: src})
	first, ok := <-wrapped.Chunks
	if !ok {
		t.Fatal("wrapped stream closed without empty_completion error")
	}
	if len(first.Payload) != 0 {
		t.Fatalf("first payload = %q, want no terminal bytes before error", first.Payload)
	}
	var authErr *coreauth.Error
	if !errors.As(first.Err, &authErr) || authErr.Code != "empty_completion" {
		t.Fatalf("first error = %v, want empty_completion", first.Err)
	}
	if _, ok = <-wrapped.Chunks; ok {
		t.Fatal("wrapped stream emitted chunks after empty_completion error")
	}
}

func TestWrapStreamEmptyCompletionRejectsZeroChunkStream(t *testing.T) {
	src := make(chan coreexecutor.StreamChunk)
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

func TestWrapStreamEmptyCompletionWithholdsSplitTerminalFrames(t *testing.T) {
	fragments := [][]byte{
		[]byte("da"),
		[]byte("ta: {\"type\":\"response.com"),
		[]byte("pleted\",\"response\":{\"output\":[],\"usage\":{\"output_tokens\":0}}}\n\n"),
		[]byte("data: [DO"),
		[]byte("NE]\n"),
		[]byte("\n"),
	}
	src := make(chan coreexecutor.StreamChunk, len(fragments))
	for _, fragment := range fragments {
		src <- coreexecutor.StreamChunk{Payload: fragment}
	}
	close(src)

	wrapped := wrapStreamEmptyCompletion(context.Background(), &coreexecutor.StreamResult{Chunks: src})
	first, ok := <-wrapped.Chunks
	if !ok {
		t.Fatal("wrapped split stream closed without empty_completion error")
	}
	if len(first.Payload) != 0 {
		t.Fatalf("first payload = %q, want no split terminal bytes before error", first.Payload)
	}
	var authErr *coreauth.Error
	if !errors.As(first.Err, &authErr) || authErr.Code != "empty_completion" {
		t.Fatalf("first error = %v, want empty_completion", first.Err)
	}
	if _, ok = <-wrapped.Chunks; ok {
		t.Fatal("wrapped stream emitted chunks after empty_completion error")
	}
}

func TestWrapStreamEmptyCompletionForwardsSplitMeaningfulOutputInOrder(t *testing.T) {
	fragments := [][]byte{
		[]byte("da"),
		[]byte("ta: {\"choices\":[{\"delta\":{\"content\":"),
		[]byte("\"hello\"},\"finish_reason\":null}]}\n"),
		[]byte("\n"),
	}
	src := make(chan coreexecutor.StreamChunk, len(fragments))
	for _, fragment := range fragments {
		src <- coreexecutor.StreamChunk{Payload: fragment}
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	wrapped := wrapStreamEmptyCompletion(ctx, &coreexecutor.StreamResult{Chunks: src})
	for _, fragment := range fragments {
		assertStreamPayload(t, wrapped.Chunks, fragment)
	}
	close(src)
	if _, ok := <-wrapped.Chunks; ok {
		t.Fatal("wrapped stream emitted an unexpected trailing chunk")
	}
}

func TestWrapStreamEmptyCompletionForwardsMeaningfulOutputInOrder(t *testing.T) {
	emptyFrame := []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":null}]}\n\n")
	contentFrame := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n")
	src := make(chan coreexecutor.StreamChunk, 2)
	src <- coreexecutor.StreamChunk{Payload: emptyFrame}
	src <- coreexecutor.StreamChunk{Payload: contentFrame}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	wrapped := wrapStreamEmptyCompletion(ctx, &coreexecutor.StreamResult{Chunks: src})
	assertStreamPayload(t, wrapped.Chunks, emptyFrame)
	assertStreamPayload(t, wrapped.Chunks, contentFrame)
	close(src)
	if _, ok := <-wrapped.Chunks; ok {
		t.Fatal("wrapped stream emitted an unexpected trailing chunk")
	}
}

func TestWrapStreamEmptyCompletionForwardsUnrecognizedStreamPromptly(t *testing.T) {
	firstPayload := []byte("opaque: first\n")
	secondPayload := []byte("opaque: second\n")
	src := make(chan coreexecutor.StreamChunk, 1)
	src <- coreexecutor.StreamChunk{Payload: firstPayload}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	wrapped := wrapStreamEmptyCompletion(ctx, &coreexecutor.StreamResult{Chunks: src})
	assertStreamPayload(t, wrapped.Chunks, firstPayload)
	src <- coreexecutor.StreamChunk{Payload: secondPayload}
	assertStreamPayload(t, wrapped.Chunks, secondPayload)
	close(src)
	if _, ok := <-wrapped.Chunks; ok {
		t.Fatal("wrapped stream emitted an unexpected trailing chunk")
	}
}

func TestWrapStreamEmptyCompletionWithholdsMetadataBeforeUpstreamError(t *testing.T) {
	upstreamErr := errors.New("upstream failed")
	src := make(chan coreexecutor.StreamChunk, 2)
	src <- coreexecutor.StreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":null}]}\n\n")}
	src <- coreexecutor.StreamChunk{Err: upstreamErr}
	close(src)

	wrapped := wrapStreamEmptyCompletion(context.Background(), &coreexecutor.StreamResult{Chunks: src})
	first, ok := <-wrapped.Chunks
	if !ok {
		t.Fatal("wrapped stream closed without upstream error")
	}
	if len(first.Payload) != 0 {
		t.Fatalf("first payload = %q, want no metadata before upstream error", first.Payload)
	}
	if !errors.Is(first.Err, upstreamErr) {
		t.Fatalf("first error = %v, want %v", first.Err, upstreamErr)
	}
	if _, ok = <-wrapped.Chunks; ok {
		t.Fatal("wrapped stream emitted chunks after upstream error")
	}
}

func TestWrapStreamEmptyCompletionPreservesContentBeforeUpstreamError(t *testing.T) {
	metadata := []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":null}]}\n\n")
	content := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n")
	upstreamErr := errors.New("upstream failed")
	src := make(chan coreexecutor.StreamChunk, 3)
	src <- coreexecutor.StreamChunk{Payload: metadata}
	src <- coreexecutor.StreamChunk{Payload: content}
	src <- coreexecutor.StreamChunk{Err: upstreamErr}
	close(src)

	wrapped := wrapStreamEmptyCompletion(context.Background(), &coreexecutor.StreamResult{Chunks: src})
	assertStreamPayload(t, wrapped.Chunks, metadata)
	assertStreamPayload(t, wrapped.Chunks, content)
	errorChunk, ok := <-wrapped.Chunks
	if !ok {
		t.Fatal("wrapped stream closed before upstream error")
	}
	if !errors.Is(errorChunk.Err, upstreamErr) {
		t.Fatalf("error = %v, want %v", errorChunk.Err, upstreamErr)
	}
	if _, ok = <-wrapped.Chunks; ok {
		t.Fatal("wrapped stream emitted chunks after upstream error")
	}
}

func TestWrapStreamEmptyCompletionPreservesNilResults(t *testing.T) {
	if got := wrapStreamEmptyCompletion(context.Background(), nil); got != nil {
		t.Fatalf("wrapStreamEmptyCompletion(nil) = %#v, want nil", got)
	}

	result := &coreexecutor.StreamResult{Headers: http.Header{"X-Test": []string{"value"}}}
	if got := wrapStreamEmptyCompletion(context.Background(), result); got != result {
		t.Fatalf("wrapStreamEmptyCompletion(nil chunks) = %#v, want original result", got)
	}
}

func TestWrapStreamEmptyCompletionStopsWhenContextCanceled(t *testing.T) {
	src := make(chan coreexecutor.StreamChunk, 1)
	src <- coreexecutor.StreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":null}]}\n\n")}
	ctx, cancel := context.WithCancel(context.Background())
	wrapped := wrapStreamEmptyCompletion(ctx, &coreexecutor.StreamResult{Chunks: src})
	cancel()

	select {
	case _, ok := <-wrapped.Chunks:
		if ok {
			t.Fatal("wrapped stream emitted a chunk after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("wrapped stream did not close after cancellation")
	}
}

func assertStreamPayload(t *testing.T, chunks <-chan coreexecutor.StreamChunk, want []byte) {
	t.Helper()
	select {
	case chunk, ok := <-chunks:
		if !ok {
			t.Fatalf("stream closed before payload %q", want)
		}
		if chunk.Err != nil {
			t.Fatalf("chunk error = %v, want payload %q", chunk.Err, want)
		}
		if string(chunk.Payload) != string(want) {
			t.Fatalf("payload = %q, want %q", chunk.Payload, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for payload %q", want)
	}
}
