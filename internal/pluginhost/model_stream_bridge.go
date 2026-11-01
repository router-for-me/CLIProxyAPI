package pluginhost

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
)

type modelStreamBridge struct {
	next    atomic.Uint64
	mu      sync.Mutex
	streams map[string]modelStreamEntry
}

type modelStreamEntry struct {
	pluginID        string
	instance        *hostCallbackInstance
	ownerCallbackID string
	chunks          <-chan handlers.ModelExecutionChunk
	cancel          context.CancelFunc
}

func newModelStreamBridge() *modelStreamBridge {
	return &modelStreamBridge{streams: make(map[string]modelStreamEntry)}
}

func (b *modelStreamBridge) open(pluginID string, instance *hostCallbackInstance, ownerCallbackID string, chunks <-chan handlers.ModelExecutionChunk, cancel context.CancelFunc) string {
	if b == nil || chunks == nil {
		if cancel != nil {
			cancel()
		}
		return ""
	}
	id := strconv.FormatUint(b.next.Add(1), 10)
	b.mu.Lock()
	b.streams[id] = modelStreamEntry{
		pluginID:        strings.TrimSpace(pluginID),
		instance:        instance,
		ownerCallbackID: ownerCallbackID,
		chunks:          chunks,
		cancel:          cancel,
	}
	b.mu.Unlock()
	return id
}

func (b *modelStreamBridge) read(ctx context.Context, pluginID string, instance *hostCallbackInstance, id string) (handlers.ModelExecutionChunk, bool, error) {
	if b == nil {
		return handlers.ModelExecutionChunk{}, true, fmt.Errorf("model stream bridge is unavailable")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return handlers.ModelExecutionChunk{}, true, fmt.Errorf("model stream id is required")
	}
	pluginID = strings.TrimSpace(pluginID)
	b.mu.Lock()
	entry, ok := b.streams[id]
	b.mu.Unlock()
	if !ok || entry.chunks == nil {
		return handlers.ModelExecutionChunk{}, true, nil
	}
	if entry.pluginID != pluginID || entry.instance != instance {
		return handlers.ModelExecutionChunk{}, true, fmt.Errorf("model stream %s is not open", id)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		b.close(pluginID, instance, id)
		return handlers.ModelExecutionChunk{}, true, ctx.Err()
	case chunk, okRead := <-entry.chunks:
		if !okRead {
			b.close(pluginID, instance, id)
			return handlers.ModelExecutionChunk{}, true, nil
		}
		if chunk.Err != nil {
			b.close(pluginID, instance, id)
			return chunk, true, nil
		}
		return chunk, false, nil
	}
}

func (b *modelStreamBridge) close(pluginID string, instance *hostCallbackInstance, id string) {
	if b == nil {
		return
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	pluginID = strings.TrimSpace(pluginID)
	b.mu.Lock()
	entry, ok := b.streams[id]
	if !ok || entry.pluginID != pluginID || entry.instance != instance {
		b.mu.Unlock()
		return
	}
	delete(b.streams, id)
	b.mu.Unlock()
	if entry.cancel != nil {
		entry.cancel()
	}
}

func (b *modelStreamBridge) closeInstance(pluginID string, instance *hostCallbackInstance) {
	if b == nil || instance == nil {
		return
	}
	b.closeMatching(pluginID, instance, false)
}

func (b *modelStreamBridge) closePlugin(pluginID string) {
	if b == nil || strings.TrimSpace(pluginID) == "" {
		return
	}
	b.closeMatching(pluginID, nil, false)
}

func (b *modelStreamBridge) closeAll() {
	if b == nil {
		return
	}
	b.closeMatching("", nil, true)
}

func (b *modelStreamBridge) closeMatching(pluginID string, instance *hostCallbackInstance, all bool) {
	pluginID = strings.TrimSpace(pluginID)
	b.mu.Lock()
	entries := make([]modelStreamEntry, 0)
	for id, entry := range b.streams {
		if !all && (entry.pluginID != pluginID || (instance != nil && entry.instance != instance)) {
			continue
		}
		delete(b.streams, id)
		entries = append(entries, entry)
	}
	b.mu.Unlock()
	for _, entry := range entries {
		if entry.cancel != nil {
			entry.cancel()
		}
	}
}
