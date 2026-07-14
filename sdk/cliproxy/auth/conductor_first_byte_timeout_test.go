package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// The first-byte timeout must surface as a retryable 504 so the existing
// OAuth-refresh / model-pool / credential-rotation / handler bootstrap-retry path
// treats it as an ordinary recoverable bootstrap failure.
func TestStreamFirstByteTimeoutError_RetryableGatewayTimeout(t *testing.T) {
	err := streamFirstByteTimeoutError(30 * time.Second)

	if err.Code != "stream_first_byte_timeout" {
		t.Fatalf("Code = %q, want stream_first_byte_timeout", err.Code)
	}
	if err.HTTPStatus != http.StatusGatewayTimeout {
		t.Fatalf("HTTPStatus = %d, want %d", err.HTTPStatus, http.StatusGatewayTimeout)
	}
	if !err.Retryable {
		t.Fatal("first-byte timeout must be Retryable so bootstrap-retry can recover")
	}
	if err.StatusCode() != http.StatusGatewayTimeout {
		t.Fatalf("StatusCode() = %d, want %d", err.StatusCode(), http.StatusGatewayTimeout)
	}
	// It must be a 5xx so the handler's bootstrapEligible retries it, and must not
	// be treated as a request-invalid (which would return immediately, no retry).
	if isRequestInvalidError(err) {
		t.Fatal("first-byte timeout must not be treated as request-invalid")
	}
}

// readStreamBootstrap must abort when its context deadline fires while the upstream
// stays silent — this is the mechanism the first-byte timeout relies on.
func TestReadStreamBootstrap_ContextTimeoutOnSilentStream(t *testing.T) {
	ch := make(chan cliproxyexecutor.StreamChunk) // never receives a chunk
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	start := time.Now()
	buffered, closed, err := readStreamBootstrap(ctx, ch)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a context error on a silent stream")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if closed {
		t.Fatal("closed should be false on timeout")
	}
	if buffered != nil {
		t.Fatalf("buffered should be nil on timeout, got %v", buffered)
	}
	if elapsed > time.Second {
		t.Fatalf("readStreamBootstrap blocked too long: %s", elapsed)
	}
}

// Empty heartbeat chunks must not satisfy the bootstrap: it must keep waiting until
// the first non-empty payload, so the first-byte timeout measures real output only.
func TestReadStreamBootstrap_SkipsEmptyUntilFirstPayload(t *testing.T) {
	ch := make(chan cliproxyexecutor.StreamChunk, 3)
	ch <- cliproxyexecutor.StreamChunk{Payload: nil}        // empty heartbeat
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("")} // empty heartbeat
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("hello")}

	buffered, closed, err := readStreamBootstrap(context.Background(), ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if closed {
		t.Fatal("closed should be false when a payload arrives")
	}
	if len(buffered) != 3 {
		t.Fatalf("buffered len = %d, want 3 (2 empty + 1 payload)", len(buffered))
	}
	if string(buffered[len(buffered)-1].Payload) != "hello" {
		t.Fatalf("last buffered payload = %q, want %q", buffered[len(buffered)-1].Payload, "hello")
	}
}
