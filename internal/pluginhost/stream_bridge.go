package pluginhost

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type streamBridge struct {
	next    atomic.Uint64
	mu      sync.Mutex
	streams map[string]chan pluginapi.ExecutorStreamChunk
}

type rpcStreamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
	Error    string `json:"error,omitempty"`
}

type rpcStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

func newStreamBridge() *streamBridge {
	return &streamBridge{streams: make(map[string]chan pluginapi.ExecutorStreamChunk)}
}

func (b *streamBridge) open(ctx context.Context) (string, <-chan pluginapi.ExecutorStreamChunk, func()) {
	if b == nil {
		chunks := make(chan pluginapi.ExecutorStreamChunk)
		close(chunks)
		return "", chunks, func() {}
	}
	id := strconv.FormatUint(b.next.Add(1), 10)
	chunks := make(chan pluginapi.ExecutorStreamChunk, 16)
	b.mu.Lock()
	b.streams[id] = chunks
	b.mu.Unlock()
	cleanup := func() {
		b.close(id, "")
	}
	if ctx != nil && ctx.Done() != nil {
		go func() {
			<-ctx.Done()
			b.close(id, ctx.Err().Error())
		}()
	}
	return id, chunks, cleanup
}

func (b *streamBridge) emit(ctx context.Context, id string, chunk pluginapi.ExecutorStreamChunk) error {
	if b == nil || id == "" {
		return fmt.Errorf("stream id is required")
	}
	b.mu.Lock()
	chunks := b.streams[id]
	b.mu.Unlock()
	if chunks == nil {
		return fmt.Errorf("stream %s is not open", id)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case chunks <- chunk:
		return nil
	}
}

func (b *streamBridge) close(id string, errorMessage string) {
	if b == nil || id == "" {
		return
	}
	b.mu.Lock()
	chunks := b.streams[id]
	delete(b.streams, id)
	b.mu.Unlock()
	if chunks == nil {
		return
	}
	if errorMessage != "" {
		chunks <- pluginapi.ExecutorStreamChunk{Err: fmt.Errorf("%s", errorMessage)}
	}
	close(chunks)
}
