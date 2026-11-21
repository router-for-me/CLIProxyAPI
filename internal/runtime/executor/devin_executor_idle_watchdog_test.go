package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func devinIdleTestRequest() (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	payload := []byte(`{"input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]}]}`)
	req := cliproxyexecutor.Request{Model: "devin/swe-2", Payload: payload}
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatInteractions,
		ResponseFormat:  sdktranslator.FormatInteractions,
		OriginalRequest: payload,
		Stream:          true,
	}
	return req, opts
}

func assertDevinIdleRetryable(t *testing.T, err error, wantMessage string) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want retryable upstream failure")
	}
	var statusCoder cliproxyexecutor.StatusError
	if !errors.As(err, &statusCoder) || statusCoder.StatusCode() != http.StatusBadGateway {
		t.Fatalf("status = %v, want 502; err=%v", statusCoder, err)
	}
	if !bytes.Contains([]byte(err.Error()), []byte(wantMessage)) {
		t.Fatalf("error = %q, want substring %q", err.Error(), wantMessage)
	}
	var truncated cliproxyexecutor.StreamTruncatedError
	if errors.As(err, &truncated) && truncated.IsStreamTruncated() {
		t.Fatalf("error %T is stream-truncated and will not fail over", err)
	}
}

// TestStreamDevinFramesIdleWatchdogAbortsDanglingStream guards issue #7442:
// a Devin upstream that accepts the turn and then never sends another frame
// must be aborted by the frame idle watchdog instead of hanging until the
// caller gives up, and the abort must fail over as a retryable 502.
func TestStreamDevinFramesIdleWatchdogAbortsDanglingStream(t *testing.T) {
	t.Parallel()

	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })

	exec := &DevinExecutor{streamIdleTimeout: 200 * time.Millisecond}
	out := make(chan cliproxyexecutor.StreamChunk, 4)
	go func() {
		defer close(out)
		exec.streamDevinFrames(
			context.Background(),
			reader,
			cliproxyexecutor.Request{Model: "devin/swe-2"},
			cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatInteractions},
			"swe-2-high",
			sdktranslator.FormatInteractions,
			nil,
			out,
		)
	}()

	select {
	case chunk, ok := <-out:
		if !ok {
			t.Fatal("stream closed without an idle error")
		}
		assertDevinIdleRetryable(t, chunk.Err, devinStreamIdleMessage)
		if len(chunk.Payload) > 0 {
			t.Fatalf("idle abort emitted payload %s", chunk.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dangling connect stream was not aborted")
	}
}

// TestDevinExecutorExecuteStreamIdleAborts guards issue #7442: the idle
// watchdog runs on frame gaps, not on the request deadline.
func TestDevinExecutorExecuteStreamIdleAborts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/connect+proto")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	exec := NewDevinExecutor(nil)
	exec.streamIdleTimeout = 200 * time.Millisecond
	auth := &cliproxyauth.Auth{
		ID:       "devin-idle",
		Provider: "devin",
		Attributes: map[string]string{
			"api_key":  "idle-key",
			"base_url": server.URL,
		},
	}
	req, opts := devinIdleTestRequest()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	started := time.Now()
	result, err := exec.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	select {
	case chunk, ok := <-result.Chunks:
		if !ok {
			t.Fatal("stream closed without an idle error")
		}
		assertDevinIdleRetryable(t, chunk.Err, devinStreamIdleMessage)
		if time.Since(started) > 2*time.Second {
			t.Fatalf("idle abort took %s, want the frame watchdog rather than the request deadline", time.Since(started))
		}
	case <-ctx.Done():
		t.Fatal("dangling connect stream waited until the request context ended")
	}
}
