package auth

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// newIssue7723Selection builds a selection bound to a real execution scope so the
// Home concurrency slot is observable through selection.Active().
func newIssue7723Selection(t *testing.T) *HomeDispatchSelection {
	t.Helper()
	registry := executionregistry.New()
	t.Cleanup(func() {
		drainCtx, cancelDrain := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancelDrain()
		_ = registry.Drain(drainCtx)
	})
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		t.Fatal(errBegin)
	}
	scope, errInstall := registry.Install(pending, executionregistry.ScopeSpec{})
	if errInstall != nil {
		t.Fatal(errInstall)
	}
	selection, errSelection := newHomeDispatchSelection(&Auth{ID: "home-auth"}, nil, "home", scope)
	if errSelection != nil {
		t.Fatal(errSelection)
	}
	return selection
}

// Nil result or nil Chunks must release the Home dispatch slot via selection.End,
// not only the attempt context. Otherwise the accounted concurrency slot leaks.
func TestIssue7723WrapHomeStreamNilResultEndsSelection(t *testing.T) {
	selection := newIssue7723Selection(t)

	var released atomic.Bool
	out := wrapHomeStream(context.Background(), nil, selection, func() { released.Store(true) })
	if out != nil {
		t.Fatalf("wrapHomeStream() = %#v, want nil result passthrough", out)
	}
	if !released.Load() {
		t.Fatal("releaseAttempt was not invoked for nil result")
	}
	if selection.Active() {
		t.Fatal("selection still active after nil stream result; Home slot leaked")
	}
}

func TestIssue7723WrapHomeStreamNilChunksEndsSelection(t *testing.T) {
	selection := newIssue7723Selection(t)

	var released atomic.Bool
	result := &cliproxyexecutor.StreamResult{Chunks: nil}
	out := wrapHomeStream(context.Background(), result, selection, func() { released.Store(true) })
	if out != result {
		t.Fatalf("wrapHomeStream() = %#v, want original result passthrough", out)
	}
	if !released.Load() {
		t.Fatal("releaseAttempt was not invoked for nil chunks")
	}
	if selection.Active() {
		t.Fatal("selection still active after nil chunks result; Home slot leaked")
	}
}

// Cancelling ctx while an upstream producer is blocked sending on an unbuffered
// result.Chunks channel must drain the remaining chunks so the producer is not
// left blocked forever.
func TestIssue7723WrapHomeStreamCancelDrainsUpstreamChunks(t *testing.T) {
	selection := newIssue7723Selection(t)

	ctx, cancel := context.WithCancel(context.Background())
	upstream := make(chan cliproxyexecutor.StreamChunk)
	result := &cliproxyexecutor.StreamResult{Chunks: upstream}
	out := wrapHomeStream(ctx, result, selection, func() {})
	if out == nil || out.Chunks == nil {
		t.Fatal("wrapHomeStream() returned nil stream")
	}

	// Producer sends a fixed number of chunks then closes the channel. On an
	// unbuffered channel each send blocks until a reader (forwarder or drain
	// goroutine) receives it. producerDone only closes after every send succeeds,
	// so its completion proves the upstream was drained rather than abandoned.
	producerDone := make(chan struct{})
	go func() {
		defer close(upstream)
		defer close(producerDone)
		for i := 0; i < 8; i++ {
			upstream <- cliproxyexecutor.StreamChunk{Payload: []byte("x")}
		}
	}()

	// Read one chunk so the forwarder is engaged, then cancel while the producer
	// is blocked on its next send.
	<-out.Chunks
	cancel()

	// The forwarder goroutine exits on cancel; the drain goroutine must keep
	// consuming upstream so the producer finishes all sends instead of leaking.
	waitUntil(t, func() bool {
		select {
		case <-producerDone:
			return true
		default:
			return false
		}
	}, "upstream producer still blocked; stream chunks were not drained on cancel")
}

// A nil context must not panic and must still forward chunks. wrapStreamResult
// and readStreamBootstrap already guard ctx == nil; wrapHomeStream must do the
// same. A buffered, pre-closed channel proves the forwarder ran without relying
// on a sleep.
func TestIssue7723WrapHomeStreamNilContextDoesNotPanic(t *testing.T) {
	selection := newIssue7723Selection(t)

	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("hello")}
	close(chunks)
	result := &cliproxyexecutor.StreamResult{Chunks: chunks}

	out := wrapHomeStream(nil, result, selection, func() {})
	if out == nil || out.Chunks == nil {
		t.Fatal("wrapHomeStream() returned nil stream for nil ctx")
	}

	// The forwarder must deliver the buffered chunk then close the output.
	select {
	case chunk, ok := <-out.Chunks:
		if !ok {
			t.Fatal("output closed before forwarding the buffered chunk")
		}
		if string(chunk.Payload) != "hello" {
			t.Fatalf("forwarded payload = %q, want %q", chunk.Payload, "hello")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for forwarded chunk with nil ctx")
	}
	waitUntil(t, func() bool {
		_, open := <-out.Chunks
		return !open
	}, "output channel did not close after draining with nil ctx")
}

func waitUntil(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}
