package pluginhost

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// streamPayloadBuffer is how many payload chunks may be queued before emit
// waits for the consumer. The raw channel capacity is one larger so close can
// always enqueue a terminal error without timers or dropping it.
const streamPayloadBuffer = 16

type streamBridge struct {
	next    atomic.Uint64
	mu      sync.Mutex
	space   *sync.Cond
	streams map[string]chan pluginapi.ExecutorStreamChunk
}

type rpcStreamEmitRequest struct {
	StreamID     string                    `json:"stream_id"`
	Payload      []byte                    `json:"payload,omitempty"`
	Error        string                    `json:"error,omitempty"`
	ErrorDetails *pluginapi.HostModelError `json:"error_details,omitempty"`
}

type rpcStreamCloseRequest struct {
	StreamID     string                    `json:"stream_id"`
	Error        string                    `json:"error,omitempty"`
	ErrorDetails *pluginapi.HostModelError `json:"error_details,omitempty"`
}

func newStreamBridge() *streamBridge {
	b := &streamBridge{streams: make(map[string]chan pluginapi.ExecutorStreamChunk)}
	b.space = sync.NewCond(&b.mu)
	return b
}

func (b *streamBridge) open(ctx context.Context) (string, <-chan pluginapi.ExecutorStreamChunk, func()) {
	if b == nil {
		chunks := make(chan pluginapi.ExecutorStreamChunk)
		close(chunks)
		return "", chunks, func() {}
	}
	id := strconv.FormatUint(b.next.Add(1), 10)
	// +1 slot reserved for close's terminal error (emit never fills it).
	raw := make(chan pluginapi.ExecutorStreamChunk, streamPayloadBuffer+1)
	out := make(chan pluginapi.ExecutorStreamChunk)
	abort := make(chan struct{})
	relayDone := make(chan struct{})
	b.mu.Lock()
	b.streams[id] = raw
	b.mu.Unlock()

	// Relay raw -> out and wake emitters after each delivery so emit can use
	// the payload slots freed by the consumer (without polling or timers).
	go func() {
		defer close(relayDone)
		defer close(out)
		for {
			var chunk pluginapi.ExecutorStreamChunk
			var ok bool
			select {
			case <-abort:
				return
			case chunk, ok = <-raw:
				if !ok {
					return
				}
			}
			select {
			case <-abort:
				return
			case out <- chunk:
			}
			b.mu.Lock()
			b.space.Broadcast()
			b.mu.Unlock()
		}
	}()

	var abortOnce sync.Once
	cleanup := func() {
		abortOnce.Do(func() { close(abort) })
		b.close(id, "", nil)
		<-relayDone
	}
	if ctx != nil && ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				cleanup()
			case <-abort:
			}
		}()
	}
	return id, out, cleanup
}

func (b *streamBridge) emit(ctx context.Context, id string, chunk pluginapi.ExecutorStreamChunk) error {
	if b == nil || id == "" {
		return fmt.Errorf("stream id is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Wake Cond waiters when the request context is canceled (no stream timer).
	stop := context.AfterFunc(ctx, func() {
		b.mu.Lock()
		b.space.Broadcast()
		b.mu.Unlock()
	})
	if stop != nil {
		defer stop()
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	for {
		raw := b.streams[id]
		if raw == nil {
			return fmt.Errorf("stream %s is not open", id)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// Keep one slot free so close can always deliver a terminal error.
		if len(raw) < streamPayloadBuffer {
			raw <- chunk
			return nil
		}
		b.space.Wait()
	}
}

func (b *streamBridge) close(id string, errorMessage string, errorDetails *pluginapi.HostModelError) {
	if b == nil || id == "" {
		return
	}
	b.mu.Lock()
	raw := b.streams[id]
	delete(b.streams, id)
	if raw != nil {
		// emit never occupies the reserved slot, so this send cannot block and
		// cannot be overtaken by a concurrent emit (both hold b.mu).
		if errorDetails != nil || errorMessage != "" {
			var err error
			if errorDetails != nil {
				err = newPluginStreamError(errorDetails, errorMessage)
			} else {
				err = fmt.Errorf("%s", errorMessage)
			}
			raw <- pluginapi.ExecutorStreamChunk{Err: err}
		}
		close(raw)
	}
	b.space.Broadcast()
	b.mu.Unlock()
}
