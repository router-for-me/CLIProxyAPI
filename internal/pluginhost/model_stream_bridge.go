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
	next     atomic.Uint64
	mu       sync.Mutex
	streams  map[string]modelStreamEntry
	inflight map[uint64]modelStreamInflight
}

type modelStreamInflight struct {
	pluginID         string
	callbackPluginID string
	instance         *hostCallbackInstance
	callbackInstance *hostCallbackInstance
	cancel           context.CancelFunc
}

type modelStreamEntry struct {
	pluginID         string
	callbackPluginID string
	instance         *hostCallbackInstance
	callbackInstance *hostCallbackInstance
	ownerCallbackID  string
	chunks           <-chan handlers.ModelExecutionChunk
	cancel           context.CancelFunc
	stopScopeCleanup func()
}

func newModelStreamBridge() *modelStreamBridge {
	return &modelStreamBridge{
		streams:  make(map[string]modelStreamEntry),
		inflight: make(map[uint64]modelStreamInflight),
	}
}

func (b *modelStreamBridge) begin(pluginID, callbackPluginID string, instance, callbackInstance *hostCallbackInstance, cancel context.CancelFunc) (uint64, bool) {
	if b == nil {
		if cancel != nil {
			cancel()
		}
		return 0, false
	}
	pluginID = strings.TrimSpace(pluginID)
	callbackPluginID = strings.TrimSpace(callbackPluginID)
	b.mu.Lock()
	if (instance != nil && instance.closed.Load()) || (callbackInstance != nil && callbackInstance.closed.Load()) {
		b.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return 0, false
	}
	if b.inflight == nil {
		b.inflight = make(map[uint64]modelStreamInflight)
	}
	id := b.next.Add(1)
	b.inflight[id] = modelStreamInflight{
		pluginID:         pluginID,
		callbackPluginID: callbackPluginID,
		instance:         instance,
		callbackInstance: callbackInstance,
		cancel:           cancel,
	}
	b.mu.Unlock()
	return id, true
}

func (b *modelStreamBridge) abort(inflightID uint64) {
	if b == nil || inflightID == 0 {
		return
	}
	b.mu.Lock()
	entry, ok := b.inflight[inflightID]
	delete(b.inflight, inflightID)
	b.mu.Unlock()
	if ok && entry.cancel != nil {
		entry.cancel()
	}
}

func (b *modelStreamBridge) complete(inflightID uint64, pluginID string, instance *hostCallbackInstance, ownerCallbackID string, chunks <-chan handlers.ModelExecutionChunk, cancel context.CancelFunc) string {
	if b == nil || chunks == nil {
		b.abort(inflightID)
		if cancel != nil {
			cancel()
		}
		return ""
	}
	id := strconv.FormatUint(b.next.Add(1), 10)
	pluginID = strings.TrimSpace(pluginID)
	b.mu.Lock()
	inflightEntry, stillInflight := b.inflight[inflightID]
	delete(b.inflight, inflightID)
	if !stillInflight || (instance != nil && instance.closed.Load()) || (inflightEntry.callbackInstance != nil && inflightEntry.callbackInstance.closed.Load()) {
		b.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return ""
	}
	if b.streams == nil {
		b.streams = make(map[string]modelStreamEntry)
	}
	b.streams[id] = modelStreamEntry{
		pluginID:         pluginID,
		callbackPluginID: inflightEntry.callbackPluginID,
		instance:         instance,
		callbackInstance: inflightEntry.callbackInstance,
		ownerCallbackID:  ownerCallbackID,
		chunks:           chunks,
		cancel:           cancel,
	}
	b.mu.Unlock()
	return id
}

func (b *modelStreamBridge) open(pluginID string, instance *hostCallbackInstance, ownerCallbackID string, chunks <-chan handlers.ModelExecutionChunk, cancel context.CancelFunc) string {
	if b == nil || chunks == nil {
		if cancel != nil {
			cancel()
		}
		return ""
	}
	id := strconv.FormatUint(b.next.Add(1), 10)
	pluginID = strings.TrimSpace(pluginID)
	b.mu.Lock()
	if instance != nil && instance.closed.Load() {
		b.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return ""
	}
	if b.streams == nil {
		b.streams = make(map[string]modelStreamEntry)
	}
	b.streams[id] = modelStreamEntry{
		pluginID:        pluginID,
		instance:        instance,
		ownerCallbackID: ownerCallbackID,
		chunks:          chunks,
		cancel:          cancel,
	}
	b.mu.Unlock()
	return id
}

func (b *modelStreamBridge) setScopeCleanup(pluginID string, instance *hostCallbackInstance, id string, stop func()) bool {
	if b == nil || stop == nil {
		if stop != nil {
			stop()
		}
		return false
	}
	id = strings.TrimSpace(id)
	pluginID = strings.TrimSpace(pluginID)
	b.mu.Lock()
	entry, ok := b.streams[id]
	if ok && entry.pluginID == pluginID && entry.instance == instance && entry.stopScopeCleanup == nil {
		entry.stopScopeCleanup = stop
		b.streams[id] = entry
		b.mu.Unlock()
		return true
	}
	b.mu.Unlock()
	stop()
	return false
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
	closeModelStreamEntry(entry)
}

func (b *modelStreamBridge) closeInstance(pluginID string, instance *hostCallbackInstance) {
	if instance == nil {
		b.closePlugin(pluginID)
		return
	}
	instance.closed.Store(true)
	if b == nil {
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

func matchesModelStreamOwner(pluginID, callbackPluginID string, instance, callbackInstance *hostCallbackInstance, targetPluginID string, targetInstance *hostCallbackInstance) bool {
	pluginMatch := pluginID == targetPluginID || (pluginID == "" && callbackPluginID == targetPluginID)
	if !pluginMatch {
		return false
	}
	if targetInstance == nil {
		return true
	}
	return instance == targetInstance || (instance == nil && callbackInstance == targetInstance)
}

func (b *modelStreamBridge) closeMatching(pluginID string, instance *hostCallbackInstance, all bool) {
	pluginID = strings.TrimSpace(pluginID)
	b.mu.Lock()
	entries := make([]modelStreamEntry, 0)
	for id, entry := range b.streams {
		if !all && !matchesModelStreamOwner(entry.pluginID, entry.callbackPluginID, entry.instance, entry.callbackInstance, pluginID, instance) {
			continue
		}
		if all {
			if entry.instance != nil {
				entry.instance.closed.Store(true)
			}
			if entry.callbackInstance != nil {
				entry.callbackInstance.closed.Store(true)
			}
		}
		delete(b.streams, id)
		entries = append(entries, entry)
	}
	inflightCancels := make([]context.CancelFunc, 0)
	for id, entry := range b.inflight {
		if !all && !matchesModelStreamOwner(entry.pluginID, entry.callbackPluginID, entry.instance, entry.callbackInstance, pluginID, instance) {
			continue
		}
		if all {
			if entry.instance != nil {
				entry.instance.closed.Store(true)
			}
			if entry.callbackInstance != nil {
				entry.callbackInstance.closed.Store(true)
			}
		}
		delete(b.inflight, id)
		if entry.cancel != nil {
			inflightCancels = append(inflightCancels, entry.cancel)
		}
	}
	b.mu.Unlock()
	for _, cancel := range inflightCancels {
		cancel()
	}
	for _, entry := range entries {
		closeModelStreamEntry(entry)
	}
}

func closeModelStreamEntry(entry modelStreamEntry) {
	if entry.stopScopeCleanup != nil {
		entry.stopScopeCleanup()
	}
	if entry.cancel != nil {
		entry.cancel()
	}
}
