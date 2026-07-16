package auth

import (
	"context"
	"errors"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// When the client context is already cancelled AND a chunk is waiting, Go's
// select would pick at random and could drop a chunk carrying a real upstream
// status (e.g. a 429 delivered just before the client aborted), leaving the
// account uncooled. readStreamBootstrap must prefer the ready chunk so the real
// status survives; only when nothing is waiting does it report cancellation.
func TestReadStreamBootstrap_PrefersReadyStatusOverCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancellation already visible before the read

	realErr := errors.New("HTTP 429 rate limited")
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Err: realErr}
	close(ch)

	_, _, err := readStreamBootstrap(ctx, ch)
	if !errors.Is(err, realErr) {
		t.Fatalf("readStreamBootstrap returned %v, want the real upstream error %v (a coincident cancel must not drop a ready status)", err, realErr)
	}
}

// A ready payload chunk is likewise preferred over the cancellation, so a
// bootstrap that actually produced a first byte is not misreported as cancelled.
func TestReadStreamBootstrap_PrefersReadyPayloadOverCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {}\n\n")}

	buffered, closed, err := readStreamBootstrap(ctx, ch)
	if err != nil {
		t.Fatalf("err = %v, want nil (a ready payload must win over the cancel)", err)
	}
	if closed {
		t.Fatalf("closed = true, want false")
	}
	if len(buffered) != 1 || len(buffered[0].Payload) == 0 {
		t.Fatalf("buffered payload not returned: %+v", buffered)
	}
}

// With nothing waiting on a cancelled context, cancellation is reported (status
// 0), so a genuinely abandoned request with no upstream signal is not recorded
// as an account failure.
func TestReadStreamBootstrap_ReportsCancelWhenNothingReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ch := make(chan cliproxyexecutor.StreamChunk) // open, empty — nothing ready

	_, _, err := readStreamBootstrap(ctx, ch)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled when no chunk is waiting", err)
	}
}
