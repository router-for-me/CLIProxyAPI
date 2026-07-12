package pluginhost

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestStreamBridgeCloseDeliversTerminalErrorWhenBufferFull(t *testing.T) {
	bridge := newStreamBridge()
	id, chunks, cleanup := bridge.open(context.Background())
	defer cleanup()

	for i := 0; i < streamPayloadBuffer; i++ {
		if err := bridge.emit(context.Background(), id, pluginapi.ExecutorStreamChunk{Payload: []byte("p")}); err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		bridge.close(id, "upstream failed", &pluginapi.HostModelError{
			StatusCode: http.StatusBadGateway,
			Message:    "upstream failed",
		})
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close hung with a full payload buffer")
	}

	var sawTerminal bool
	var payloads int
	deadline := time.After(time.Second)
	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				if !sawTerminal {
					t.Fatal("stream closed with clean EOF; terminal error was dropped")
				}
				if payloads != streamPayloadBuffer {
					t.Fatalf("payloads = %d, want %d", payloads, streamPayloadBuffer)
				}
				return
			}
			if chunk.Err != nil {
				sawTerminal = true
				if se, ok := chunk.Err.(interface{ StatusCode() int }); !ok || se.StatusCode() != http.StatusBadGateway {
					t.Fatalf("terminal error = %#v, want status 502", chunk.Err)
				}
			} else {
				payloads++
			}
		case <-deadline:
			t.Fatal("timed out draining stream")
		}
	}
}

func TestStreamBridgeCleanupStopsRelayWithoutConsumer(t *testing.T) {
	bridge := newStreamBridge()
	id, chunks, cleanup := bridge.open(context.Background())

	if err := bridge.emit(context.Background(), id, pluginapi.ExecutorStreamChunk{Payload: []byte("pending")}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	cleanup()

	select {
	case chunk, ok := <-chunks:
		if ok {
			t.Fatalf("chunk after cleanup = %#v, want closed stream", chunk)
		}
	case <-time.After(time.Second):
		t.Fatal("relay did not stop after cleanup")
	}
}

func TestStreamBridgeEmitStopsAfterClose(t *testing.T) {
	bridge := newStreamBridge()
	id, chunks, _ := bridge.open(context.Background())

	bridge.close(id, "closed", nil)
	if err := bridge.emit(context.Background(), id, pluginapi.ExecutorStreamChunk{Payload: []byte("late")}); err == nil {
		t.Fatal("emit after close should fail")
	}

	var sawErr bool
	for chunk := range chunks {
		if chunk.Err != nil {
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatal("expected terminal error from close")
	}
}

func TestStreamBridgeConcurrentEmitDuringClose(t *testing.T) {
	bridge := newStreamBridge()
	id, chunks, _ := bridge.open(context.Background())

	for i := 0; i < streamPayloadBuffer; i++ {
		if err := bridge.emit(context.Background(), id, pluginapi.ExecutorStreamChunk{Payload: []byte("p")}); err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = bridge.emit(context.Background(), id, pluginapi.ExecutorStreamChunk{Payload: []byte("extra")})
	}()
	go func() {
		defer wg.Done()
		bridge.close(id, "boom", &pluginapi.HostModelError{StatusCode: http.StatusBadGateway, Message: "boom"})
	}()
	wg.Wait()

	var sawTerminal bool
	for chunk := range chunks {
		if chunk.Err != nil {
			sawTerminal = true
		}
	}
	if !sawTerminal {
		t.Fatal("expected terminal error to survive concurrent emit")
	}
}

func TestStreamBridgeEmitUnblocksWhenConsumerDrains(t *testing.T) {
	bridge := newStreamBridge()
	id, chunks, cleanup := bridge.open(context.Background())
	defer cleanup()

	for i := 0; i < streamPayloadBuffer; i++ {
		if err := bridge.emit(context.Background(), id, pluginapi.ExecutorStreamChunk{Payload: []byte("p")}); err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}

	emitDone := make(chan error, 1)
	go func() {
		emitDone <- bridge.emit(context.Background(), id, pluginapi.ExecutorStreamChunk{Payload: []byte("more")})
	}()

	// Consumer drains one payload so the blocked emit can use a free slot.
	select {
	case <-chunks:
	case <-time.After(time.Second):
		t.Fatal("timed out reading first payload")
	}

	select {
	case err := <-emitDone:
		if err != nil {
			t.Fatalf("emit after drain: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("emit stayed blocked after consumer drained")
	}
}
