package pluginhost

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
)

type modelStreamBridge struct {
	next    atomic.Uint64
	mu      sync.Mutex
	streams map[string]modelStreamEntry
}

type modelStreamEntry struct {
	ownerCallbackID string
	chunks          <-chan handlers.ModelExecutionChunk
	cancel          context.CancelFunc
}

func newModelStreamBridge() *modelStreamBridge {
	return &modelStreamBridge{streams: make(map[string]modelStreamEntry)}
}

func (b *modelStreamBridge) open(ownerCallbackID string, chunks <-chan handlers.ModelExecutionChunk, cancel context.CancelFunc) string {
	if b == nil || chunks == nil {
		if cancel != nil {
			cancel()
		}
		return ""
	}
	id := strconv.FormatUint(b.next.Add(1), 10)
	b.mu.Lock()
	b.streams[id] = modelStreamEntry{
		ownerCallbackID: ownerCallbackID,
		chunks:          chunks,
		cancel:          cancel,
	}
	b.mu.Unlock()
	return id
}

func (b *modelStreamBridge) read(ctx context.Context, id string) (handlers.ModelExecutionChunk, bool, error) {
	if b == nil {
		return handlers.ModelExecutionChunk{}, true, fmt.Errorf("model stream bridge is unavailable")
	}
	if id == "" {
		return handlers.ModelExecutionChunk{}, true, fmt.Errorf("model stream id is required")
	}
	b.mu.Lock()
	entry, ok := b.streams[id]
	b.mu.Unlock()
	if !ok || entry.chunks == nil {
		return handlers.ModelExecutionChunk{}, true, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		b.close(id)
		return handlers.ModelExecutionChunk{}, true, ctx.Err()
	case chunk, okRead := <-entry.chunks:
		if !okRead {
			b.close(id)
			return handlers.ModelExecutionChunk{}, true, nil
		}
		if chunk.Err != nil {
			b.close(id)
			return chunk, true, nil
		}
		return chunk, false, nil
	}
}

func (b *modelStreamBridge) close(id string) {
	if b == nil || id == "" {
		return
	}
	b.mu.Lock()
	entry := b.streams[id]
	delete(b.streams, id)
	b.mu.Unlock()
	if entry.cancel != nil {
		entry.cancel()
	}
}
