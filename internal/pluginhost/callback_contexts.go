package pluginhost

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
)

type callbackContextRegistry struct {
	next     atomic.Uint64
	mu       sync.RWMutex
	contexts map[string]callbackContextEntry
}

type callbackContextEntry struct {
	ctx     context.Context
	cleanup []func()
}

func newCallbackContextRegistry() *callbackContextRegistry {
	return &callbackContextRegistry{contexts: make(map[string]callbackContextEntry)}
}

func (r *callbackContextRegistry) open(ctx context.Context) (string, func()) {
	if r == nil {
		return "", func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	id := strconv.FormatUint(r.next.Add(1), 10)
	r.mu.Lock()
	r.contexts[id] = callbackContextEntry{ctx: ctx}
	r.mu.Unlock()

	var once sync.Once
	return id, func() {
		once.Do(func() {
			var cleanup []func()
			r.mu.Lock()
			entry := r.contexts[id]
			delete(r.contexts, id)
			r.mu.Unlock()
			cleanup = entry.cleanup
			for _, fn := range cleanup {
				if fn != nil {
					fn()
				}
			}
		})
	}
}

func (r *callbackContextRegistry) addCleanup(id string, cleanup func()) bool {
	if r == nil || id == "" || cleanup == nil {
		return false
	}
	r.mu.Lock()
	entry, ok := r.contexts[id]
	if ok {
		entry.cleanup = append(entry.cleanup, cleanup)
		r.contexts[id] = entry
	}
	r.mu.Unlock()
	if !ok {
		cleanup()
		return false
	}
	return true
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
	if h == nil || h.callbackContexts == nil {
		return "", func() {}
	}
	return h.callbackContexts.open(ctx)
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

func (h *Host) resolveCallbackContext(id string, fallback context.Context) context.Context {
	if h == nil || h.callbackContexts == nil {
		if fallback == nil {
			return context.Background()
		}
		return fallback
	}
	return h.callbackContexts.resolve(id, fallback)
}
