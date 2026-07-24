package pluginhost

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type streamBridgeNotifyContext struct {
	context.Context
	ready chan struct{}
	once  sync.Once
}

func (c *streamBridgeNotifyContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.ready) })
	return c.Context.Done()
}

func TestStreamBridgeCloseUnblocksPendingEmit(t *testing.T) {
	bridge := newStreamBridge()
	streamID, chunks, _ := bridge.open(context.Background())

	for range streamBridgeBufferSize {
		if err := bridge.emit(context.Background(), streamID, pluginapi.ExecutorStreamChunk{Payload: []byte("buffered")}); err != nil {
			t.Fatalf("fill stream buffer: %v", err)
		}
	}

	emitCtx := &streamBridgeNotifyContext{
		Context: context.Background(),
		ready:   make(chan struct{}),
	}
	emitDone := make(chan error, 1)
	go func() {
		emitDone <- bridge.emit(emitCtx, streamID, pluginapi.ExecutorStreamChunk{Payload: []byte("blocked")})
	}()

	select {
	case <-emitCtx.ready:
	case <-time.After(time.Second):
		t.Fatal("emit did not reach the blocked send")
	}
	select {
	case err := <-emitDone:
		t.Fatalf("emit returned while the stream buffer was full: %v", err)
	default:
	}

	bridge.close(streamID, "", nil)

	select {
	case err := <-emitDone:
		if err == nil || !strings.Contains(err.Error(), "is not open") {
			t.Fatalf("emit error = %v, want stream-not-open error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not unblock the pending emit")
	}

	chunkCount := 0
	for range chunks {
		chunkCount++
	}
	if chunkCount != streamBridgeBufferSize {
		t.Fatalf("delivered chunks = %d, want %d buffered chunks without the rejected emit", chunkCount, streamBridgeBufferSize)
	}
}

func TestStreamBridgeEmitUsesAcceptedPumpResultAfterContextCancellation(t *testing.T) {
	for range 1000 {
		ctx, cancel := context.WithCancel(context.Background())
		stream := &streamBridgeStream{
			emits:  make(chan streamBridgeEmit),
			closed: make(chan struct{}),
		}
		go func() {
			request := <-stream.emits
			cancel()
			request.done <- nil
		}()

		if err := stream.emit(ctx, pluginapi.ExecutorStreamChunk{Payload: []byte("accepted")}); err != nil {
			t.Fatalf("accepted emit returned error: %v", err)
		}
	}
}

func TestStreamBridgeAbortClosesSaturatedStreamWithoutConsumer(t *testing.T) {
	bridge := newStreamBridge()
	streamID, chunks, cleanup := bridge.open(context.Background())
	bridge.mu.Lock()
	stream := bridge.streams[streamID]
	bridge.mu.Unlock()

	for range streamBridgeBufferSize {
		if err := bridge.emit(context.Background(), streamID, pluginapi.ExecutorStreamChunk{Payload: []byte("buffered")}); err != nil {
			t.Fatalf("fill stream buffer: %v", err)
		}
	}

	cleanup()

	select {
	case <-stream.finished:
	case <-time.After(time.Second):
		t.Fatal("abort left the saturated stream pump running")
	}
	if _, ok := <-chunks; ok {
		t.Fatal("aborted stream retained buffered chunks")
	}
}

func TestStreamBridgeCleanupAbortsPendingGracefulClose(t *testing.T) {
	bridge := newStreamBridge()
	streamID, chunks, cleanup := bridge.open(context.Background())
	bridge.mu.Lock()
	stream := bridge.streams[streamID]
	bridge.mu.Unlock()

	for range streamBridgeBufferSize {
		if err := bridge.emit(context.Background(), streamID, pluginapi.ExecutorStreamChunk{Payload: []byte("buffered")}); err != nil {
			t.Fatalf("fill stream buffer: %v", err)
		}
	}
	bridge.close(streamID, "plugin stream failed", nil)

	cleanup()

	select {
	case <-stream.finished:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not abort the graceful close after the stream was removed")
	}
	if _, ok := <-chunks; ok {
		t.Fatal("cleanup retained queued chunks after aborting the graceful close")
	}
}

func TestStreamBridgeCloseDeliversTerminalError(t *testing.T) {
	bridge := newStreamBridge()
	streamID, chunks, _ := bridge.open(context.Background())

	bridge.close(streamID, "plugin stream failed", &pluginapi.HostModelError{
		StatusCode: http.StatusBadGateway,
		Headers:    http.Header{"Retry-After": []string{"5"}},
		Message:    "plugin stream failed",
	})

	chunk, ok := <-chunks
	if !ok {
		t.Fatal("stream closed before terminal error")
	}
	if chunk.Err == nil || chunk.Err.Error() != "plugin stream failed" {
		t.Fatalf("terminal error = %v, want plugin stream failed", chunk.Err)
	}
	statusErr, ok := chunk.Err.(interface{ StatusCode() int })
	if !ok || statusErr.StatusCode() != http.StatusBadGateway {
		t.Fatalf("terminal error status = %#v, want 502", chunk.Err)
	}
	headerErr, ok := chunk.Err.(interface{ Headers() http.Header })
	if !ok || headerErr.Headers().Get("Retry-After") != "5" {
		t.Fatalf("terminal error headers = %#v, want retry-after", chunk.Err)
	}
	if _, ok = <-chunks; ok {
		t.Fatal("stream remains open after terminal error")
	}
}

func TestStreamBridgeClosePreservesTerminalErrorWhenBufferIsFull(t *testing.T) {
	bridge := newStreamBridge()
	streamID, chunks, _ := bridge.open(context.Background())

	for range streamBridgeBufferSize {
		if err := bridge.emit(context.Background(), streamID, pluginapi.ExecutorStreamChunk{Payload: []byte("buffered")}); err != nil {
			t.Fatalf("fill stream buffer: %v", err)
		}
	}

	closeDone := make(chan struct{})
	go func() {
		bridge.close(streamID, "plugin stream failed", &pluginapi.HostModelError{
			StatusCode: http.StatusBadGateway,
			Message:    "plugin stream failed",
		})
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("close blocked on the saturated stream")
	}

	chunkCount := 0
	var terminalErr error
	for chunk := range chunks {
		chunkCount++
		if chunk.Err != nil {
			terminalErr = chunk.Err
		}
	}
	if chunkCount != streamBridgeBufferSize+1 {
		t.Fatalf("delivered chunks = %d, want %d buffered chunks plus terminal error", chunkCount, streamBridgeBufferSize+1)
	}
	if terminalErr == nil || terminalErr.Error() != "plugin stream failed" {
		t.Fatalf("terminal error = %v, want plugin stream failed", terminalErr)
	}
	statusErr, ok := terminalErr.(interface{ StatusCode() int })
	if !ok || statusErr.StatusCode() != http.StatusBadGateway {
		t.Fatalf("terminal error status = %#v, want 502", terminalErr)
	}
}

func TestStreamBridgeEmitUnblocksOnContextCancellationWhenFull(t *testing.T) {
	bridge := newStreamBridge()
	id, _, cleanup := bridge.open(context.Background())
	defer cleanup()

	for i := 0; i < streamBridgeBufferSize; i++ {
		if err := bridge.emit(context.Background(), id, pluginapi.ExecutorStreamChunk{Payload: []byte("p")}); err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	emitDone := make(chan error, 1)
	go func() {
		emitDone <- bridge.emit(ctx, id, pluginapi.ExecutorStreamChunk{Payload: []byte("blocked")})
	}()
	cancel()

	select {
	case err := <-emitDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("emit error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("emit did not stop after context cancellation")
	}
}

func TestStreamBridgeUsesPerStreamSignals(t *testing.T) {
	bridge := newStreamBridge()
	firstID, _, firstCleanup := bridge.open(context.Background())
	defer firstCleanup()
	secondID, _, secondCleanup := bridge.open(context.Background())
	defer secondCleanup()

	bridge.mu.Lock()
	firstSignal := bridge.streams[firstID].emits
	secondSignal := bridge.streams[secondID].emits
	bridge.mu.Unlock()
	if firstSignal == secondSignal {
		t.Fatal("streams share a space signal")
	}
}
