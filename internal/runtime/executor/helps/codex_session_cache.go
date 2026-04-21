// Package helps provides shared helpers for runtime executors.
//
// codex_session_cache.go implements a lightweight, TTL-bounded cache of
// "logical session" state for Codex upstream requests. Each entry tracks:
//
//   - SessionID: stable identifier sent as the "Session_id" request header.
//   - TurnState: the sticky routing token the upstream returns in the
//     "x-codex-turn-state" response header, which must be replayed on the next
//     request in the same turn/session.
//   - TurnMetadata: per-turn observability metadata returned by the upstream in
//     the "x-codex-turn-metadata" response header.
//
// This mirrors the behavior of codex-rs's ModelClientSession/ModelClientState,
// which captures these values from responses and re-sends them on subsequent
// requests so the upstream load balancer can pin the conversation to a
// specific shard and warm prompt cache.
package helps

import (
	"strings"
	"sync"
	"time"
)

// CodexSessionState is the cached set of session-scoped headers that must be
// echoed back to the upstream on subsequent requests belonging to the same
// logical session.
type CodexSessionState struct {
	SessionID    string
	TurnState    string
	TurnMetadata string
	Expire       time.Time
}

// codexSessionTTL controls how long a session entry remains valid after its
// last update. 30 minutes matches the upper bound of a typical chat turn
// lifetime while being short enough to free memory from abandoned sessions.
const codexSessionTTL = 30 * time.Minute

// codexSessionCleanupInterval controls how often a background goroutine purges
// expired entries. One shard is swept per tick to amortize the cost.
const codexSessionCleanupInterval = 10 * time.Minute

var (
	codexSessionStore       = newShardedStringMap[CodexSessionState]()
	codexSessionCleanupOnce sync.Once
)

func startCodexSessionCleanup() {
	go func() {
		ticker := time.NewTicker(codexSessionCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			codexSessionStore.cleanupNextShard(time.Now(), func(v CodexSessionState, now time.Time) bool {
				return v.Expire.Before(now)
			})
		}
	}()
}

// GetCodexSession returns the cached session state for key, or ok=false if the
// entry is missing or has expired.
func GetCodexSession(key string) (CodexSessionState, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return CodexSessionState{}, false
	}
	codexSessionCleanupOnce.Do(startCodexSessionCleanup)
	state, ok := codexSessionStore.load(key)
	if !ok {
		return CodexSessionState{}, false
	}
	if state.Expire.Before(time.Now()) {
		return CodexSessionState{}, false
	}
	return state, true
}

// SetCodexSession stores the full session state verbatim (and refreshes TTL
// if Expire is zero). Callers normally prefer UpdateCodexSession which does a
// read-modify-write under the shard lock.
func SetCodexSession(key string, state CodexSessionState) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	codexSessionCleanupOnce.Do(startCodexSessionCleanup)
	if state.Expire.IsZero() {
		state.Expire = time.Now().Add(codexSessionTTL)
	}
	codexSessionStore.store(key, state)
}

// UpdateCodexSession performs a read-modify-write on the session entry keyed
// by key. The mutator receives the existing state (or a zero value) and can
// update any fields; the TTL is always refreshed on write. If after mutation
// every string field is empty the entry is deleted to avoid leaking garbage.
func UpdateCodexSession(key string, mutate func(*CodexSessionState)) {
	key = strings.TrimSpace(key)
	if key == "" || mutate == nil {
		return
	}
	codexSessionCleanupOnce.Do(startCodexSessionCleanup)

	shard := codexSessionStore.shardForKey(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()

	state := shard.entries[key]
	mutate(&state)

	state.SessionID = strings.TrimSpace(state.SessionID)
	state.TurnState = strings.TrimSpace(state.TurnState)
	state.TurnMetadata = strings.TrimSpace(state.TurnMetadata)

	if state.SessionID == "" && state.TurnState == "" && state.TurnMetadata == "" {
		delete(shard.entries, key)
		return
	}

	state.Expire = time.Now().Add(codexSessionTTL)
	shard.entries[key] = state
}

// ClearCodexSessions wipes the entire cache. Intended for tests only.
func ClearCodexSessions() {
	codexSessionStore.clear()
}
