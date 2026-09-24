package auth

import (
	"strings"
	"sync"
	"time"
)

// model_pool_cooldown.go implements the pool-wide per-model cooldown aggregate
// (Phase 2 F2): when at least two AND at least half of a provider's
// non-disabled credentials are cooling for the SAME canonical model, further
// requests for that model fail fast with the model-cooldown error instead of
// rotating through every credential. Like the pool breaker, state is
// in-memory by design — a restart starts every pool clean.
//
// The aggregate is count-based and stateless about WHY a credential is
// cooling: record() re-counts the caller's auth slice at record time. The
// caller (Manager.MarkResult in Task 6) invokes it AFTER updating the auth's
// model state, so the auth being recorded is already part of the count, and a
// success re-records with the refreshed slice — the count then falls below
// the threshold and the entry is dropped implicitly. There is intentionally
// no separate "clear" call: recording is always a full re-evaluation for one
// (provider, model) pair.
type modelPoolCooldowns struct {
	mu      sync.Mutex
	entries map[string]modelPoolCooldownEntry // key: provider + "\x00" + canonicalModelKey(model)
}

// modelPoolCooldownEntry is one (provider, model) aggregate. contributions
// mirrors the per-auth cooling-until values observed at the last record() so
// dropAuth can retire a removed credential's contribution. Auth IDs are
// expected to be non-empty: an ID-less auth still counts toward the cooling
// tally but is invisible to dropAuth (no behavior change is planned for
// ID-less auths, which the manager does not produce).
type modelPoolCooldownEntry struct {
	deadline      time.Time
	updatedAt     time.Time
	contributions map[string]time.Time // authID -> cooling-until for this provider+model
}

func newModelPoolCooldowns() *modelPoolCooldowns {
	return &modelPoolCooldowns{entries: make(map[string]modelPoolCooldownEntry)}
}

func modelPoolCooldownKey(provider, model string) string {
	return provider + "\x00" + canonicalModelKey(model)
}

// record re-evaluates the pool state for one (provider, model) pair from the
// caller's full auth slice. A non-disabled same-provider auth counts as
// cooling when its ModelState for the canonical model has Quota.Exceeded with
// NextRetryAfter still in the future (the auth being recorded is already
// reflected in the slice — record AFTER the state update). Passing a slice
// where the counts fall below the threshold (including on success) deletes
// any existing entry: the pool has recovered. now anchors every comparison.
func (m *modelPoolCooldowns) record(auths []*Auth, provider, model string, now time.Time) {
	provider = strings.TrimSpace(provider)
	modelKey := canonicalModelKey(model)
	if provider == "" || modelKey == "" {
		return
	}
	key := modelPoolCooldownKey(provider, modelKey)

	var total, cooling int
	var deadline time.Time
	contributions := make(map[string]time.Time)
	for _, auth := range auths {
		if auth == nil || auth.Provider != provider || auth.Disabled {
			continue
		}
		total++
		state, ok := auth.ModelStates[modelKey]
		if !ok || state == nil {
			continue
		}
		// A stale "exceeded" past its retry time no longer counts as cooling.
		if !state.Quota.Exceeded || !state.NextRetryAfter.After(now) {
			continue
		}
		cooling++
		if auth.ID != "" {
			contributions[auth.ID] = state.NextRetryAfter
		}
		if state.NextRetryAfter.After(deadline) {
			deadline = state.NextRetryAfter
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if cooling < 2 || cooling*2 < total {
		// Below the threshold (or the pool shrank): the aggregate lifts.
		delete(m.entries, key)
		return
	}
	m.entries[key] = modelPoolCooldownEntry{
		deadline:      deadline,
		updatedAt:     now,
		contributions: contributions,
	}
}

// block reports the pool-wide fail-fast deadline for a (provider, model)
// across any of providers, lazily expiring entries whose deadline has passed.
func (m *modelPoolCooldowns) block(providers []string, model string, now time.Time) (time.Time, bool) {
	modelKey := canonicalModelKey(model)
	if modelKey == "" {
		return time.Time{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, provider := range providers {
		provider = strings.TrimSpace(provider)
		if provider == "" {
			continue
		}
		key := modelPoolCooldownKey(provider, modelKey)
		entry, ok := m.entries[key]
		if !ok {
			continue
		}
		if entry.deadline.After(now) {
			return entry.deadline, true
		}
		// Lazy expiry: the deadline passed, so the aggregate no longer blocks.
		delete(m.entries, key)
	}
	return time.Time{}, false
}

// dropAuth removes a removed credential's contribution from every entry.
// Entries whose contribution count falls below the aggregate's own cooldown
// minimum (2) no longer gate selection and are dropped.
func (m *modelPoolCooldowns) dropAuth(authID string) {
	if authID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, entry := range m.entries {
		if _, ok := entry.contributions[authID]; !ok {
			continue
		}
		delete(entry.contributions, authID)
		if len(entry.contributions) < 2 {
			delete(m.entries, key)
		}
	}
}
