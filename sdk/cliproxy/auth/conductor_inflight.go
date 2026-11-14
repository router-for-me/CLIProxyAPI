package auth

import (
	"context"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// InFlightSnapshot is an immutable view of standalone in-flight executions.
type InFlightSnapshot struct {
	InFlight        int64
	OldestStartedAt time.Time
	ByProvider      map[string]int64
}

// inFlightTracker counts in-flight local executions for standalone instances.
// It stays disabled while Home mode is active because the execution registry
// already tracks Home-dispatched executions for that lifetime.
type inFlightTracker struct {
	mu             sync.Mutex
	count          int64
	oldestStarted  time.Time
	byProvider     map[string]int64
	inflightScopes map[*inFlightScope]time.Time
}

func newInFlightTracker() *inFlightTracker {
	return &inFlightTracker{
		byProvider:     make(map[string]int64),
		inflightScopes: make(map[*inFlightScope]time.Time),
	}
}

// inFlightScope represents one tracked execution until its end is called.
type inFlightScope struct {
	tracker  *inFlightTracker
	provider string
	done     sync.Once
}

// setProvider updates the provider attribution of an in-flight scope without
// changing the total count or its start time.
func (s *inFlightScope) setProvider(provider string) {
	if s == nil || s.tracker == nil {
		return
	}
	t := s.tracker
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.inflightScopes[s]; !ok || s.provider == provider {
		return
	}
	if next := t.byProvider[s.provider] - 1; next > 0 {
		t.byProvider[s.provider] = next
	} else {
		delete(t.byProvider, s.provider)
	}
	s.provider = provider
	t.byProvider[provider]++
}

// end releases the scope exactly once.
func (s *inFlightScope) end() {
	if s == nil || s.tracker == nil {
		return
	}
	s.done.Do(func() {
		t := s.tracker
		t.mu.Lock()
		defer t.mu.Unlock()
		startedAt, ok := t.inflightScopes[s]
		if !ok {
			return
		}
		delete(t.inflightScopes, s)
		t.count--
		if next := t.byProvider[s.provider] - 1; next > 0 {
			t.byProvider[s.provider] = next
		} else {
			delete(t.byProvider, s.provider)
		}
		if t.count == 0 {
			t.oldestStarted = time.Time{}
		} else if startedAt.Equal(t.oldestStarted) {
			t.recomputeOldestLocked()
		}
	})
}

func (t *inFlightTracker) recomputeOldestLocked() {
	var oldest time.Time
	for _, startedAt := range t.inflightScopes {
		if oldest.IsZero() || startedAt.Before(oldest) {
			oldest = startedAt
		}
	}
	t.oldestStarted = oldest
}

// begin tracks a new execution. The returned scope must be ended exactly once.
func (t *inFlightTracker) begin(provider string) *inFlightScope {
	if t == nil {
		return nil
	}
	scope := &inFlightScope{tracker: t, provider: provider}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	t.inflightScopes[scope] = now
	t.count++
	t.byProvider[provider]++
	if t.oldestStarted.IsZero() || now.Before(t.oldestStarted) {
		t.oldestStarted = now
	}
	return scope
}

// snapshot copies the current in-flight state into an immutable view.
func (t *inFlightTracker) snapshot() InFlightSnapshot {
	if t == nil {
		return InFlightSnapshot{ByProvider: map[string]int64{}}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	byProvider := make(map[string]int64, len(t.byProvider))
	for provider, count := range t.byProvider {
		byProvider[provider] = count
	}
	return InFlightSnapshot{
		InFlight:        t.count,
		OldestStartedAt: t.oldestStarted,
		ByProvider:      byProvider,
	}
}

// InFlightSnapshot returns the current standalone in-flight execution counts.
// It reports an empty snapshot while Home mode is enabled, where the execution
// registry is the authoritative in-flight source instead.
func (m *Manager) InFlightSnapshot() InFlightSnapshot {
	if m == nil || m.inFlightTracker == nil || m.HomeEnabled() {
		return InFlightSnapshot{ByProvider: map[string]int64{}}
	}
	return m.inFlightTracker.snapshot()
}

// beginInFlight starts tracking one local execution. The returned scope must be
// ended exactly once; it is nil-safe and ends as a no-op in Home mode.
func (m *Manager) beginInFlight(provider string) *inFlightScope {
	if m == nil || m.inFlightTracker == nil || m.HomeEnabled() {
		return nil
	}
	return m.inFlightTracker.begin(provider)
}

// wrapInFlightStreamResult extends an in-flight scope until the returned
// stream finishes or ctx is canceled, whichever comes first.
func wrapInFlightStreamResult(result *cliproxyexecutor.StreamResult, scope *inFlightScope, ctx context.Context) *cliproxyexecutor.StreamResult {
	if result == nil || scope == nil {
		if scope != nil {
			scope.end()
		}
		return result
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		// Release the in-flight scope as soon as forwarding stops (stream end
		// or cancellation). The output channel closes only after the inner
		// channel is fully drained so callers keep the original happens-before
		// between stream end and upstream result bookkeeping.
		defer scope.end()
		if result.Chunks == nil {
			close(out)
			return
		}
		forward := true
		for forward {
			select {
			case <-ctx.Done():
				forward = false
			case chunk, ok := <-result.Chunks:
				if !ok {
					close(out)
					return
				}
				select {
				case <-ctx.Done():
					forward = false
				case out <- chunk:
				}
			}
		}
		// After cancellation, drain the inner channel on the same goroutine so
		// the inner stream wrapper finishes its bookkeeping before out closes.
		// The scope is released explicitly here before the drain can block;
		// the deferred end above remains as the safety net for other exits.
		scope.end()
		for range result.Chunks {
		}
		close(out)
	}()
	return &cliproxyexecutor.StreamResult{Headers: result.Headers, Chunks: out}
}

// inFlightScopeContextKey carries the request in-flight scope to inner
// selection code so the provider attribution can be corrected after pick.
type inFlightScopeContextKey struct{}

// contextWithInFlightScope returns a context carrying the scope, or ctx when
// scope is nil.
func contextWithInFlightScope(ctx context.Context, scope *inFlightScope) context.Context {
	if ctx == nil || scope == nil {
		return ctx
	}
	return context.WithValue(ctx, inFlightScopeContextKey{}, scope)
}

// updateInFlightScopeProvider re-attributes the request scope to the actually
// selected provider after auth selection. It is a no-op without a scope.
func updateInFlightScopeProvider(ctx context.Context, provider string) {
	if ctx == nil {
		return
	}
	scope, _ := ctx.Value(inFlightScopeContextKey{}).(*inFlightScope)
	scope.setProvider(provider)
}
