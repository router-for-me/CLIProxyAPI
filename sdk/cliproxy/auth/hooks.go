package auth

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
)

// hookChain keeps the original SDK hook and independently removable observers.
// Callbacks run without the subscription lock and may subscribe or unsubscribe.
type hookChain struct {
	primary   Hook
	mu        sync.Mutex
	observers atomic.Pointer[[]*hookSubscription]
}

type hookSubscription struct{ hook Hook }

// AddHook subscribes to lifecycle events without replacing the constructor hook.
// The returned function is idempotent. An already dispatched callback may finish
// after removal; callbacks should only perform short, non-blocking work.
func (m *Manager) AddHook(hook Hook) func() {
	if m == nil || m.hook == nil || hook == nil {
		return func() {}
	}
	chain := m.hook
	sub := &hookSubscription{hook: hook}
	chain.mu.Lock()
	var observers []*hookSubscription
	if current := chain.observers.Load(); current != nil {
		observers = slices.Clone(*current)
	}
	observers = append(observers, sub)
	chain.observers.Store(&observers)
	chain.mu.Unlock()
	return func() {
		chain.mu.Lock()
		defer chain.mu.Unlock()
		current := chain.observers.Load()
		if current == nil {
			return
		}
		remaining := slices.DeleteFunc(slices.Clone(*current), func(entry *hookSubscription) bool { return entry == sub })
		if len(remaining) == 0 {
			chain.observers.Store(nil)
		} else {
			chain.observers.Store(&remaining)
		}
	}
}

func (h *hookChain) OnAuthRegistered(ctx context.Context, auth *Auth) {
	observers := h.observers.Load()
	if observers == nil {
		h.primary.OnAuthRegistered(ctx, auth)
		return
	}
	h.primary.OnAuthRegistered(ctx, auth.Clone())
	for _, observer := range *observers {
		observer.hook.OnAuthRegistered(ctx, auth.Clone())
	}
}

func (h *hookChain) OnAuthUpdated(ctx context.Context, auth *Auth) {
	observers := h.observers.Load()
	if observers == nil {
		h.primary.OnAuthUpdated(ctx, auth)
		return
	}
	h.primary.OnAuthUpdated(ctx, auth.Clone())
	for _, observer := range *observers {
		observer.hook.OnAuthUpdated(ctx, auth.Clone())
	}
}

func (h *hookChain) OnResult(ctx context.Context, result Result) {
	observers := h.observers.Load()
	h.primary.OnResult(ctx, result)
	if observers != nil {
		for _, observer := range *observers {
			observer.hook.OnResult(ctx, result)
		}
	}
}
