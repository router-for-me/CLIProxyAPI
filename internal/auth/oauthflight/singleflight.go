// Package oauthflight provides a generic single-flight helper for collapsing
// concurrent OAuth token refresh attempts for the same auth identity into a
// single in-flight HTTP call.
//
// Rotating refresh_token semantics (used by xAI, OpenAI Codex, and others)
// guarantee that the server burns the supplied refresh_token immediately on
// success and issues a new one. If N goroutines call the refresh endpoint
// concurrently with the same refresh_token, only one succeeds; the other N-1
// receive refresh_token_reused errors and the credential is poisoned.
//
// oauthflight.Do collapses concurrent callers for the same authID into one
// invocation of fn, returning the same (T, error) pair to all waiters. This
// is in-process-only — multi-replica deployments behind a shared auths/ store
// can still race; documented as an accepted hazard.
package oauthflight

import "sync"

// inflight tracks one ongoing refresh call.
type inflight[T any] struct {
	done  chan struct{}
	value T
	err   error
}

// group holds the per-authID inflight registry for a single T.
//
// We use *one* package-level map keyed by authID alone — the helper is
// expected to be used by callers that already pin T per call site (e.g.,
// Grok callers always use T = *RefreshResult). Mixing T values per authID
// is a programmer error.
var (
	mu     sync.Mutex
	groups = make(map[string]any)
)

// testAfterCommit is called after a goroutine has committed to either the
// leader path (inserted a new entry) or the waiter path (found an existing
// entry and is about to block on entry.done). It is nil in production and
// set only by tests to synchronise goroutines. The hook is called with mu
// already released.
var testAfterCommit func()

// reset clears all in-flight entries and the test hook. Intended for tests only.
func reset() {
	mu.Lock()
	groups = make(map[string]any)
	mu.Unlock()
	testAfterCommit = nil
}

// Do collapses concurrent calls keyed by authID into a single execution of
// fn. The first concurrent caller invokes fn; subsequent callers block until
// fn returns, then receive the same (T, error) pair. Once fn returns, the
// in-flight entry is removed so the next caller starts a fresh execution.
//
// authID must be a stable identifier for the credential (e.g., the storage
// key of the auth entry). Empty authID is allowed but degenerate — it would
// serialize all calls across the process. Callers should treat that as a
// bug; we don't panic to keep this helper as boring as possible.
func Do[T any](authID string, fn func() (T, error)) (T, error) {
	mu.Lock()
	if existing, ok := groups[authID]; ok {
		mu.Unlock()
		if hook := testAfterCommit; hook != nil {
			hook()
		}
		entry := existing.(*inflight[T])
		<-entry.done
		return entry.value, entry.err
	}

	entry := &inflight[T]{done: make(chan struct{})}
	groups[authID] = entry
	mu.Unlock()

	if hook := testAfterCommit; hook != nil {
		hook()
	}

	value, err := fn()
	entry.value = value
	entry.err = err
	close(entry.done)

	mu.Lock()
	delete(groups, authID)
	mu.Unlock()

	return value, err
}
