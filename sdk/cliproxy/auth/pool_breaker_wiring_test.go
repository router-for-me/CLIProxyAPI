package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// registerPoolBreakerAuth registers one pool auth (compound provider_key) that
// optionally opts in to the pool-level circuit breaker via the
// pool_circuit_breaker attribute.
func registerPoolBreakerAuth(t *testing.T, m *Manager, id, pool string, breaker bool) *Auth {
	t.Helper()
	attrs := map[string]string{
		"provider_key":            pool,
		AttributeEntryProviderKey: pool + ":key-71",
		AttributePoolStrategy:     "round-robin",
		AttributeAuthKind:         AuthKindAPIKey,
		AttributeAPIKey:           "k-" + id,
	}
	if breaker {
		attrs[AttributePoolCircuitBreaker] = "true"
	}
	auth := &Auth{
		ID:         id,
		Provider:   "claude",
		Status:     StatusActive,
		Attributes: attrs,
	}
	if _, errRegister := m.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register(%s) error = %v", id, errRegister)
	}
	return auth
}

func transient503Result(authID string) Result {
	return Result{
		AuthID:   authID,
		Provider: "claude",
		Model:    "claude-sonnet-4-5",
		Success:  false,
		Error: &Error{
			Code:       "upstream_error",
			Message:    "upstream unavailable",
			HTTPStatus: http.StatusServiceUnavailable,
		},
	}
}

// cleanupPoolBreakerKeys removes the given breaker keys so the global breaker
// does not leak state between tests.
func cleanupPoolBreakerKeys(t *testing.T, keys ...string) {
	t.Helper()
	t.Cleanup(func() {
		globalPoolBreaker.mu.Lock()
		for _, key := range keys {
			delete(globalPoolBreaker.pools, key)
		}
		globalPoolBreaker.mu.Unlock()
	})
}

// poolBreakerEntryState returns the raw breaker entry state for key, if any.
func poolBreakerEntryState(key string) (breakerState, time.Time, bool) {
	globalPoolBreaker.mu.Lock()
	defer globalPoolBreaker.mu.Unlock()
	entry, ok := globalPoolBreaker.pools[key]
	if !ok {
		return breakerClosed, time.Time{}, false
	}
	return entry.state, entry.openedAt.Add(entry.resetPeriod), true
}

// TestPoolBreakerWiring_OpensAndBlocksPool pins the G3 wiring: threshold
// failures fed through MarkResult open the breaker for the pool key, blocking
// selection for opted-in auths and (pool-keyed) for same-pool auths without
// the attribute, while other pools stay selectable. A success through
// MarkResult closes the breaker again.
func TestPoolBreakerWiring_OpensAndBlocksPool(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	breakerAuth := registerPoolBreakerAuth(t, manager, "auth-breaker-a", "claude:7", true)
	bystander := registerPoolBreakerAuth(t, manager, "auth-pool-no-attr", "claude:7", false)
	otherPool := registerPoolBreakerAuth(t, manager, "auth-other-pool", "claude:8", false)
	cleanupPoolBreakerKeys(t, "claude:7", "claude:8")

	for i := 0; i < poolBreakerFailureThreshold; i++ {
		manager.MarkResult(context.Background(), transient503Result(breakerAuth.ID))
	}

	// The breaker keyed by provider_key must now be OPEN with the base reset
	// period still ahead of us.
	state, deadline, ok := poolBreakerEntryState("claude:7")
	if !ok || state != breakerOpen {
		t.Fatalf("expected breaker for claude:7 to be open after threshold failures, ok=%v state=%v", ok, state)
	}
	if !deadline.After(time.Now()) {
		t.Fatalf("expected open breaker deadline %v to be in the future", deadline)
	}

	now := time.Now()
	// The opted-in auth is blocked by the breaker with a cooldown reason and
	// the breaker's deadline (not the per-auth transient cooldown).
	blocked, reason, next := isAuthBlockedForModel(breakerAuth, "claude-sonnet-4-5", now)
	if !blocked || reason != blockReasonCooldown {
		t.Fatalf("isAuthBlockedForModel(opted-in) = (%v, %v), want blocked with cooldown reason", blocked, reason)
	}
	if next.IsZero() || !next.Equal(deadline) {
		t.Fatalf("isAuthBlockedForModel(opted-in) deadline = %v, want breaker deadline %v", next, deadline)
	}

	// A same-pool auth WITHOUT the attribute is blocked too: the breaker is
	// pool-keyed. This auth carries no cooldown state of its own, so the block
	// can only come from the breaker.
	blocked, reason, next = isAuthBlockedForModel(bystander, "claude-sonnet-4-5", now)
	if !blocked || reason != blockReasonCooldown {
		t.Fatalf("isAuthBlockedForModel(same-pool bystander) = (%v, %v), want blocked with cooldown reason", blocked, reason)
	}
	if !next.Equal(deadline) {
		t.Fatalf("isAuthBlockedForModel(same-pool bystander) deadline = %v, want breaker deadline %v", next, deadline)
	}

	// The same-pool block also applies on the model-less selection path.
	blocked, reason, next = isAuthBlockedForModel(bystander, "", now)
	if !blocked || reason != blockReasonCooldown || !next.Equal(deadline) {
		t.Fatalf("isAuthBlockedForModel(same-pool bystander, no model) = (%v, %v, %v), want blocked with cooldown reason and breaker deadline", blocked, reason, next)
	}

	// A different pool stays selectable (its breaker key was never fed).
	blocked, reason, next = isAuthBlockedForModel(otherPool, "claude-sonnet-4-5", now)
	if blocked || reason != blockReasonNone || !next.IsZero() {
		t.Fatalf("isAuthBlockedForModel(other pool) = (%v, %v, %v), want selectable", blocked, reason, next)
	}

	// The snapshot surfaces the open pool.
	var snapshotRecord *PoolBreakerRecord
	for _, record := range PoolBreakerSnapshot() {
		if record.PoolKey == "claude:7" {
			record := record
			snapshotRecord = &record
		}
	}
	if snapshotRecord == nil {
		t.Fatalf("PoolBreakerSnapshot() missing claude:7 record: %+v", PoolBreakerSnapshot())
	}
	if snapshotRecord.State != "open" || snapshotRecord.Failures != poolBreakerFailureThreshold || snapshotRecord.OpenUntil.IsZero() {
		t.Fatalf("PoolBreakerSnapshot() claude:7 = %+v, want open state with failures and OpenUntil", *snapshotRecord)
	}

	// A success through MarkResult closes the breaker: the bystander (which
	// has no cooldown state of its own) becomes selectable again.
	manager.MarkResult(context.Background(), Result{
		AuthID:   breakerAuth.ID,
		Provider: "claude",
		Model:    "claude-sonnet-4-5",
		Success:  true,
	})
	if blockedAfterSuccess, _, _ := isAuthBlockedForModel(bystander, "claude-sonnet-4-5", time.Now()); blockedAfterSuccess {
		t.Fatalf("isAuthBlockedForModel(same-pool bystander) after success = blocked, want selectable")
	}
	for _, record := range PoolBreakerSnapshot() {
		if record.PoolKey == "claude:7" {
			t.Fatalf("PoolBreakerSnapshot() still contains claude:7 after success: %+v", record)
		}
	}
}

// TestPoolBreakerWiring_Reclassified403DoesNotFeedBreaker pins that the quota
// ladder (including Task 2's reclassified 403) never feeds the breaker: only
// 408/5xx count toward the pool threshold. The failing auth itself cools down
// via its own per-auth quota state; a same-pool peer without failures of its
// own must stay selectable, proving the breaker was never fed.
func TestPoolBreakerWiring_Reclassified403DoesNotFeedBreaker(t *testing.T) {
	prevReclassify := reclassifyQuota403.Load()
	reclassifyQuota403.Store(true)
	t.Cleanup(func() { reclassifyQuota403.Store(prevReclassify) })

	manager := NewManager(nil, nil, nil)
	failing := registerPoolBreakerAuth(t, manager, "auth-breaker-403", "claude:9", true)
	peer := registerPoolBreakerAuth(t, manager, "auth-403-peer", "claude:9", true)
	cleanupPoolBreakerKeys(t, "claude:9")

	for i := 0; i < poolBreakerFailureThreshold+4; i++ {
		manager.MarkResult(context.Background(), Result{
			AuthID:   failing.ID,
			Provider: "claude",
			Model:    "claude-sonnet-4-5",
			Success:  false,
			Error: &Error{
				Code:       "forbidden",
				Message:    "quota exceeded for this key",
				HTTPStatus: http.StatusForbidden,
			},
		})
	}
	for _, record := range PoolBreakerSnapshot() {
		if record.PoolKey == "claude:9" {
			t.Fatalf("PoolBreakerSnapshot() contains claude:9 after reclassified 403s: %+v", record)
		}
	}
	if blocked, _, _ := isAuthBlockedForModel(peer, "claude-sonnet-4-5", time.Now()); blocked {
		t.Fatalf("isAuthBlockedForModel(unfailed same-pool peer) = blocked, want selectable (breaker not fed by reclassified 403s)")
	}
}
