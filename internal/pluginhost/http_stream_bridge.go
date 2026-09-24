package pluginhost

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type hostHTTPStreamKey struct {
	pluginID string
	instance *hostCallbackInstance
	streamID string
}

type hostHTTPStreamBridge struct {
	next    atomic.Uint64
	mu      sync.Mutex
	streams map[hostHTTPStreamKey]hostHTTPStreamEntry
}

type hostHTTPStreamEntry struct {
	chunks  <-chan pluginapi.HTTPStreamChunk
	cancel  context.CancelFunc
	onClose func()
}

func newHostHTTPStreamBridge() *hostHTTPStreamBridge {
	return &hostHTTPStreamBridge{streams: make(map[hostHTTPStreamKey]hostHTTPStreamEntry)}
}

func (b *hostHTTPStreamBridge) open(pluginID string, instance *hostCallbackInstance, chunks <-chan pluginapi.HTTPStreamChunk, cancel context.CancelFunc, onClose func()) string {
	if b == nil || chunks == nil {
		if cancel != nil {
			cancel()
		}
		if onClose != nil {
			onClose()
		}
		return ""
	}
	id := strconv.FormatUint(b.next.Add(1), 10)
	key := hostHTTPStreamKey{pluginID: strings.TrimSpace(pluginID), instance: instance, streamID: id}
	b.mu.Lock()
	b.streams[key] = hostHTTPStreamEntry{chunks: chunks, cancel: cancel, onClose: onClose}
	b.mu.Unlock()
	return id
}

func (b *hostHTTPStreamBridge) read(ctx context.Context, pluginID string, instance *hostCallbackInstance, id string) (pluginapi.HTTPStreamChunk, bool, error) {
	if b == nil || id == "" {
		return pluginapi.HTTPStreamChunk{}, true, fmt.Errorf("http stream id is required")
	}
	key := hostHTTPStreamKey{pluginID: strings.TrimSpace(pluginID), instance: instance, streamID: strings.TrimSpace(id)}
	b.mu.Lock()
	entry := b.streams[key]
	b.mu.Unlock()
	if entry.chunks == nil {
		return pluginapi.HTTPStreamChunk{}, true, fmt.Errorf("http stream %s is not open", id)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		b.close(pluginID, instance, id)
		return pluginapi.HTTPStreamChunk{}, true, ctx.Err()
	case chunk, ok := <-entry.chunks:
		if !ok {
			b.close(pluginID, instance, id)
			return pluginapi.HTTPStreamChunk{}, true, nil
		}
		if chunk.Err != nil {
			b.close(pluginID, instance, id)
			return chunk, true, nil
		}
		return chunk, false, nil
	}
}

func (b *hostHTTPStreamBridge) close(pluginID string, instance *hostCallbackInstance, id string) {
	if b == nil || id == "" {
		return
	}
	key := hostHTTPStreamKey{pluginID: strings.TrimSpace(pluginID), instance: instance, streamID: strings.TrimSpace(id)}
	b.mu.Lock()
	entry := b.streams[key]
	delete(b.streams, key)
	b.mu.Unlock()
	closeHostHTTPStreamEntry(entry)
}

func (b *hostHTTPStreamBridge) closeInstance(pluginID string, instance *hostCallbackInstance) {
	if b == nil || instance == nil {
		return
	}
	b.closeMatching(pluginID, instance, false)
}

func (b *hostHTTPStreamBridge) closePlugin(pluginID string) {
	if b == nil || strings.TrimSpace(pluginID) == "" {
		return
	}
	b.closeMatching(pluginID, nil, false)
}

func (b *hostHTTPStreamBridge) closeAll() {
	if b == nil {
		return
	}
	b.closeMatching("", nil, true)
}

func (b *hostHTTPStreamBridge) closeMatching(pluginID string, instance *hostCallbackInstance, all bool) {
	pluginID = strings.TrimSpace(pluginID)
	b.mu.Lock()
	entries := make([]hostHTTPStreamEntry, 0)
	for key, entry := range b.streams {
		if !all && (key.pluginID != pluginID || (instance != nil && key.instance != instance)) {
			continue
		}
		delete(b.streams, key)
		entries = append(entries, entry)
	}
	b.mu.Unlock()
	for _, entry := range entries {
		closeHostHTTPStreamEntry(entry)
	}
}

func closeHostHTTPStreamEntry(entry hostHTTPStreamEntry) {
	if entry.cancel != nil {
		entry.cancel()
	}
	if entry.onClose != nil {
		entry.onClose()
	}
}
