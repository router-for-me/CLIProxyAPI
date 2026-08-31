package auth

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestExtractSessionID_ClientRequestIDIsNotASession pins audit finding B6: a
// per-request id is not a session id. X-Client-Request-Id used to map to a
// "clientreq:" affinity key, so every request carrying it created a new,
// never-reused binding. The header must fall through to the body-derived
// signals instead; with no body signals there is no session at all.
func TestExtractSessionID_ClientRequestIDIsNotASession(t *testing.T) {
	t.Parallel()

	// No other session signal: the header alone must not produce an ID.
	headers := make(http.Header)
	headers.Set("X-Client-Request-Id", "req-abc-123")
	if got := ExtractSessionID(headers, nil, nil); got != "" {
		t.Errorf("ExtractSessionID() with only X-Client-Request-Id = %q, want empty (a request id is not a session id)", got)
	}

	// With a real body signal present, the body wins because the header is no
	// longer consulted.
	headers.Set("Session-Id", "codex-session-456")
	if got := ExtractSessionID(headers, nil, nil); got != "codex:codex-session-456" {
		t.Errorf("ExtractSessionID() with Session-Id = %q, want codex:codex-session-456", got)
	}

	// Header plus prompt_cache_key body signal: body wins.
	headersOnly := http.Header{"X-Client-Request-Id": []string{"client-session"}}
	payload := `{"prompt_cache_key":"prompt-session","conversation":{"id":"conversation-session"}}`
	if got := ExtractSessionID(headersOnly, []byte(payload), nil); got != "pck:prompt-session" {
		t.Errorf("ExtractSessionID() = %q, want pck:prompt-session (X-Client-Request-Id must not override body session signals)", got)
	}
}

// TestSessionCacheBoundedEntries pins the second half of B6: SessionCache had
// no bound on the number of entries, only a per-entry alias cap, so any
// per-request-ish session signal grew the cache by one entry per request for a
// full TTL. Inserting past the cap must evict expired entries first and then
// drop the oldest live bindings rather than grow without limit.
func TestSessionCacheBoundedEntries(t *testing.T) {
	t.Parallel()

	cache := NewSessionCache(time.Hour)
	defer cache.Stop()
	const maxEntries = 10000
	const insert = maxEntries + 500

	for i := 0; i < insert; i++ {
		cache.Set(fmt.Sprintf("provider::session-%d::model", i), fmt.Sprintf("auth-%d", i%7))
	}

	cache.mu.RLock()
	got := len(cache.entries)
	cache.mu.RUnlock()
	if got > maxEntries {
		t.Fatalf("SessionCache grew past the cap: %d entries after %d inserts, want <= %d", got, insert, maxEntries)
	}

	// Drop-oldest must hold: the earliest inserted bindings are the ones
	// evicted, and the most recent inserts survive.
	if _, ok := cache.Get("provider::session-0::model"); ok {
		t.Errorf("SessionCache.Get(oldest key) = ok, want evicted once the cap is exceeded")
	}
	if _, ok := cache.Get(fmt.Sprintf("provider::session-%d::model", insert-1)); !ok {
		t.Errorf("SessionCache.Get(newest key) = missing, want present after eviction")
	}
}

// TestSessionCacheBoundedEntriesEvictsExpiredFirst verifies the cap works
// together with TTL cleanup: expired entries are reclaimed before live ones
// when the cap is exceeded.
func TestSessionCacheBoundedEntriesEvictsExpiredFirst(t *testing.T) {
	t.Parallel()

	cache := NewSessionCache(time.Hour)
	defer cache.Stop()
	const maxEntries = 10000

	// Fill with entries that are already expired. Each entry gets a zero
	// lastTouched so a buggy drop-oldest pass would reclaim them before the
	// expiry pass, but the expired-first pass makes that unreachable.
	cache.mu.Lock()
	cache.entries = make(map[string]sessionEntry)
	for i := 0; i < maxEntries; i++ {
		cache.entries[fmt.Sprintf("provider::stale-%d::model", i)] = sessionEntry{
			authID:      "auth-stale",
			expiresAt:   time.Now().Add(-time.Minute),
			aliases:     []string{fmt.Sprintf("provider::stale-%d::model", i)},
			lastTouched: time.Time{},
		}
	}
	cache.mu.Unlock()

	cache.Set("provider::fresh::model", "auth-fresh")

	cache.mu.RLock()
	got := len(cache.entries)
	_, freshOK := cache.entries["provider::fresh::model"]
	_, staleOK := cache.entries["provider::stale-0::model"]
	cache.mu.RUnlock()
	if got > maxEntries {
		t.Fatalf("SessionCache kept stale entries past the cap: %d entries, want <= %d", got, maxEntries)
	}
	if !freshOK {
		t.Error("SessionCache dropped the fresh binding while stale entries were reclaimable")
	}
	if staleOK {
		t.Error("SessionCache kept a stale entry instead of reclaiming it when over the cap")
	}
}
