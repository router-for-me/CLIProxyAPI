package pluginhost

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

type callbackContextRegistry struct {
	next        atomic.Uint64
	nextCleanup atomic.Uint64
	mu          sync.RWMutex
	contexts    map[string]callbackContextEntry
}

type callbackContextEntry struct {
	ctx      context.Context
	pluginID string
	instance *hostCallbackInstance
	closed   chan struct{}
	cleanup  []callbackContextCleanup
}

type callbackContextCleanup struct {
	id uint64
	fn func()
}

func newCallbackContextRegistry() *callbackContextRegistry {
	return &callbackContextRegistry{contexts: make(map[string]callbackContextEntry)}
}

func (r *callbackContextRegistry) open(ctx context.Context, pluginID string, instance *hostCallbackInstance) (string, func()) {
	if r == nil {
		return "", func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	pluginID = strings.TrimSpace(pluginID)
	ctx = withHostCallbackIdentity(ctx, pluginID, instance)
	r.mu.Lock()
	if instance != nil && instance.closed.Load() {
		r.mu.Unlock()
		return "", func() {}
	}
	id := strconv.FormatUint(r.next.Add(1), 10)
	closed := make(chan struct{})
	r.contexts[id] = callbackContextEntry{ctx: ctx, pluginID: pluginID, instance: instance, closed: closed}
	r.mu.Unlock()

	var once sync.Once
	return id, func() {
		once.Do(func() {
			r.mu.Lock()
			entry, exists := r.contexts[id]
			if exists {
				delete(r.contexts, id)
			}
			r.mu.Unlock()
			if exists {
				finishCallbackContextEntry(entry)
			}
		})
	}
}

func finishCallbackContextEntry(entry callbackContextEntry) {
	if entry.closed != nil {
		close(entry.closed)
	}
	for _, item := range entry.cleanup {
		if item.fn != nil {
			item.fn()
		}
	}
}

func (r *callbackContextRegistry) closeInstance(pluginID string, instance *hostCallbackInstance) {
	if instance != nil {
		instance.closed.Store(true)
	}
	if r == nil {
		return
	}
	pluginID = strings.TrimSpace(pluginID)
	r.mu.Lock()
	entries := make([]callbackContextEntry, 0)
	for id, entry := range r.contexts {
		if entry.pluginID != pluginID {
			continue
		}
		if instance != nil && entry.instance != instance {
			continue
		}
		delete(r.contexts, id)
		entries = append(entries, entry)
	}
	r.mu.Unlock()
	for _, entry := range entries {
		finishCallbackContextEntry(entry)
	}
}

func (r *callbackContextRegistry) closePlugin(pluginID string) {
	r.closeInstance(pluginID, nil)
}

func (r *callbackContextRegistry) closeAll() {
	if r == nil {
		return
	}
	r.mu.Lock()
	entries := make([]callbackContextEntry, 0, len(r.contexts))
	for id, entry := range r.contexts {
		if entry.instance != nil {
			entry.instance.closed.Store(true)
		}
		delete(r.contexts, id)
		entries = append(entries, entry)
	}
	r.mu.Unlock()
	for _, entry := range entries {
		finishCallbackContextEntry(entry)
	}
}

func (r *callbackContextRegistry) lookup(id string) (context.Context, string, *hostCallbackInstance, bool) {
	ctx, pluginID, instance, _, ok := r.lookupWithClosed(id)
	return ctx, pluginID, instance, ok
}

func (r *callbackContextRegistry) lookupWithClosed(id string) (context.Context, string, *hostCallbackInstance, <-chan struct{}, bool) {
	if r == nil {
		return nil, "", nil, nil, false
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, "", nil, nil, false
	}
	r.mu.RLock()
	entry, exists := r.contexts[id]
	r.mu.RUnlock()
	if !exists || entry.ctx == nil {
		return nil, "", nil, nil, false
	}
	return entry.ctx, strings.TrimSpace(entry.pluginID), entry.instance, entry.closed, true
}

func (r *callbackContextRegistry) pluginID(id string) string {
	_, pluginID, _, _ := r.lookup(id)
	return pluginID
}

func (r *callbackContextRegistry) addCleanup(id string, cleanup func()) bool {
	_, ok := r.addCleanupHandle(id, cleanup)
	return ok
}

func (r *callbackContextRegistry) addCleanupHandle(id string, cleanup func()) (func(), bool) {
	if r == nil || cleanup == nil {
		return func() {}, false
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return func() {}, false
	}
	cleanupID := r.nextCleanup.Add(1)
	r.mu.Lock()
	entry, exists := r.contexts[id]
	if exists {
		entry.cleanup = append(entry.cleanup, callbackContextCleanup{id: cleanupID, fn: cleanup})
		r.contexts[id] = entry
	}
	r.mu.Unlock()
	if !exists {
		cleanup()
		return func() {}, false
	}

	var once sync.Once
	remove := func() {
		once.Do(func() {
			r.mu.Lock()
			entry, exists := r.contexts[id]
			if exists {
				for index, item := range entry.cleanup {
					if item.id == cleanupID {
						entry.cleanup = append(entry.cleanup[:index], entry.cleanup[index+1:]...)
						r.contexts[id] = entry
						break
					}
				}
			}
			r.mu.Unlock()
		})
	}
	return remove, true
}

func (r *callbackContextRegistry) resolve(id string, fallback context.Context) context.Context {
	if fallback == nil {
		fallback = context.Background()
	}
	if r == nil || id == "" {
		return fallback
	}
	r.mu.RLock()
	ctx := r.contexts[id].ctx
	r.mu.RUnlock()
	if ctx == nil {
		return fallback
	}
	return ctx
}

func (h *Host) openCallbackContext(ctx context.Context) (string, func()) {
	return h.openCallbackContextForPlugin(ctx, "")
}

func (h *Host) openCallbackContextForPlugin(ctx context.Context, pluginID string) (string, func()) {
	return h.openCallbackContextForPluginInstance(ctx, pluginID, nil)
}

func (h *Host) openCallbackContextForPluginInstance(ctx context.Context, pluginID string, instance *hostCallbackInstance) (string, func()) {
	if h == nil || h.callbackContexts == nil {
		return "", func() {}
	}
	if strings.TrimSpace(pluginID) == "" {
		pluginID = hostCallbackPluginIDFromContext(ctx)
	}
	if instance == nil {
		instance = hostCallbackInstanceFromContext(ctx)
	}
	return h.callbackContexts.open(ctx, pluginID, instance)
}

func (h *Host) addCallbackCleanup(id string, cleanup func()) bool {
	if h == nil || h.callbackContexts == nil {
		if id != "" && cleanup != nil {
			cleanup()
		}
		return false
	}
	return h.callbackContexts.addCleanup(id, cleanup)
}

func (h *Host) addCallbackCleanupHandle(id string, cleanup func()) (func(), bool) {
	if h == nil || h.callbackContexts == nil {
		if id != "" && cleanup != nil {
			cleanup()
		}
		return func() {}, false
	}
	return h.callbackContexts.addCleanupHandle(id, cleanup)
}

func (h *Host) lookupCallbackContext(id string) (context.Context, string, *hostCallbackInstance, bool) {
	if h == nil || h.callbackContexts == nil {
		return nil, "", nil, false
	}
	return h.callbackContexts.lookup(id)
}

func (h *Host) lookupCallbackContextWithClosed(id string) (context.Context, string, *hostCallbackInstance, <-chan struct{}, bool) {
	if h == nil || h.callbackContexts == nil {
		return nil, "", nil, nil, false
	}
	return h.callbackContexts.lookupWithClosed(id)
}

func (h *Host) resolveCallbackContext(id string, fallback context.Context) context.Context {
	if h == nil || h.callbackContexts == nil {
		if fallback == nil {
			return context.Background()
		}
		return fallback
	}
	return h.callbackContexts.resolve(id, fallback)
}

func (h *Host) callbackContextPluginID(id string) string {
	if h == nil || h.callbackContexts == nil {
		return ""
	}
	return h.callbackContexts.pluginID(id)
}
