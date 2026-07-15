package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// An exhausted first-byte timeout must be TERMINAL: a 504 that is NOT retryable
// and is recognised by IsFirstByteTimeoutExhausted, so the credential-switch,
// request-retry, and handler bootstrap layers all surface it straight to the
// client instead of burning other credentials or replaying the whole request.
func TestFirstByteTimeoutExhaustedError_TerminalGatewayTimeout(t *testing.T) {
	err := firstByteTimeoutExhaustedError(15*time.Second, 2)

	if err.Code != firstByteTimeoutExhaustedCode {
		t.Fatalf("Code = %q, want %q", err.Code, firstByteTimeoutExhaustedCode)
	}
	if err.HTTPStatus != http.StatusGatewayTimeout {
		t.Fatalf("HTTPStatus = %d, want %d", err.HTTPStatus, http.StatusGatewayTimeout)
	}
	if err.Retryable {
		t.Fatal("exhausted first-byte timeout must NOT be retryable (it is terminal)")
	}
	if !IsFirstByteTimeoutExhausted(err) {
		t.Fatal("IsFirstByteTimeoutExhausted must recognise the exhausted error")
	}
	if isRequestInvalidError(err) {
		t.Fatal("exhausted first-byte timeout must not be treated as request-invalid")
	}
	// A genuine upstream 504 (or any other error) must NOT be misclassified as an
	// exhausted first-byte timeout, so real transient-error cooldowns still apply.
	if IsFirstByteTimeoutExhausted(&Error{HTTPStatus: http.StatusGatewayTimeout, Message: "real upstream 504"}) {
		t.Fatal("a real upstream 504 must not be classified as an exhausted first-byte timeout")
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
