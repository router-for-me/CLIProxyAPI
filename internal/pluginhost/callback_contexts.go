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
	contexts map[string]context.Context
}

func newCallbackContextRegistry() *callbackContextRegistry {
	return &callbackContextRegistry{contexts: make(map[string]context.Context)}
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
	r.contexts[id] = ctx
	r.mu.Unlock()

	var once sync.Once
	return id, func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.contexts, id)
			r.mu.Unlock()
		})
	}
}

func (r *callbackContextRegistry) resolve(id string, fallback context.Context) context.Context {
	if fallback == nil {
		fallback = context.Background()
	}
	if r == nil || id == "" {
		return fallback
	}
	r.mu.RLock()
	ctx := r.contexts[id]
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

func (h *Host) resolveCallbackContext(id string, fallback context.Context) context.Context {
	if h == nil || h.callbackContexts == nil {
		if fallback == nil {
			return context.Background()
		}
		return fallback
	}
	return h.callbackContexts.resolve(id, fallback)
}
