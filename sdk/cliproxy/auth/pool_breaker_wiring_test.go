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

// expirePoolBreakerOpen backdates an OPEN entry's openedAt so its reset period
// is already elapsed at now. The wiring path reads time.Now(), so expiry is
// simulated by moving the anchor instead of sleeping.
func expirePoolBreakerOpen(t *testing.T, key string) {
	t.Helper()
	globalPoolBreaker.mu.Lock()
	defer globalPoolBreaker.mu.Unlock()
	entry, ok := globalPoolBreaker.pools[key]
	if !ok || entry.state != breakerOpen {
		t.Fatalf("expirePoolBreakerOpen(%s): entry missing or not open (ok=%v)", key, ok)
	}
	entry.openedAt = time.Now().Add(-(entry.resetPeriod + 5*time.Second))
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

// TestPoolBreakerWiring_HalfOpenRecoveryPinsProbeAtCommit pins the fixed defect:
// after the breaker OPENs and its reset period elapses, selection reads
// (isAuthBlockedForModel, including the model-filter call that follows the
// pick) return NOT blocked repeatedly — the probe slot survives them — and
// the single probe is taken only at execution commit via admitProbe. A
// MarkResult success through the admitted probe closes the pool again.
func TestPoolBreakerWiring_HalfOpenRecoveryPinsProbeAtCommit(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	breakerAuth := registerPoolBreakerAuth(t, manager, "auth-breaker-halfopen", "claude:10", true)
	cleanupPoolBreakerKeys(t, "claude:10")

	for i := 0; i < poolBreakerFailureThreshold; i++ {
		manager.MarkResult(context.Background(), transient503Result(breakerAuth.ID))
	}
	state, deadline, ok := poolBreakerEntryState("claude:10")
	if !ok || state != breakerOpen {
		t.Fatalf("expected breaker for claude:10 to be open after threshold failures, ok=%v state=%v", ok, state)
	}
	if !deadline.After(time.Now()) {
		t.Fatalf("expected open breaker deadline %v to be in the future", deadline)
	}

	// Expire the reset period (simulated by backdating openedAt; the wiring
	// path reads time.Now()).
	expirePoolBreakerOpen(t, "claude:10")

	// The exact defect, pinned: the read path must return NOT blocked for
	// repeated calls — a second read (the model filter after the pick) must
	// not consume the first caller's probe.
	now := time.Now()
	for call := 0; call < 3; call++ {
		blocked, reason, next := isAuthBlockedForModel(breakerAuth, "claude-sonnet-4-5", now)
		if blocked || reason != blockReasonNone || !next.IsZero() {
			t.Fatalf("isAuthBlockedForModel read %d after expiry = (%v, %v, %v), want unblocked", call, blocked, reason, next)
		}
	}
	// The same holds on the model-less path.
	if blocked, _, _ := isAuthBlockedForModel(breakerAuth, "", now); blocked {
		t.Fatalf("isAuthBlockedForModel(no model) after expiry = blocked, want unblocked")
	}

	// Admission is explicit and one-shot: first commit takes the probe, a
	// second concurrent commit is denied.
	if !globalPoolBreaker.admitProbe("claude:10", time.Now()) {
		t.Fatal("expected first admitProbe to take the probe")
	}
	if globalPoolBreaker.admitProbe("claude:10", time.Now()) {
		t.Fatal("expected second admitProbe denied while probe in flight")
	}
	// While the probe is in flight, selection reads report the block with the
	// deadline anchored at admission.
	blocked, reason, next := isAuthBlockedForModel(breakerAuth, "claude-sonnet-4-5", time.Now())
	if !blocked || reason != blockReasonCooldown {
		t.Fatalf("isAuthBlockedForModel while probe in flight = (%v, %v), want blocked cooldown", blocked, reason)
	}
	if next.IsZero() || !next.After(time.Now()) {
		t.Fatalf("isAuthBlockedForModel while probe in flight deadline = %v, want future", next)
	}

	// The probe succeeds through MarkResult: the pool closes and selection
	// recovers.
	manager.MarkResult(context.Background(), Result{
		AuthID:   breakerAuth.ID,
		Provider: "claude",
		Model:    "claude-sonnet-4-5",
		Success:  true,
	})
	for _, record := range PoolBreakerSnapshot() {
		if record.PoolKey == "claude:10" {
			t.Fatalf("PoolBreakerSnapshot() still contains claude:10 after probe success: %+v", record)
		}
	}
	if blockedAfter, _, _ := isAuthBlockedForModel(breakerAuth, "claude-sonnet-4-5", time.Now()); blockedAfter {
		t.Fatalf("isAuthBlockedForModel after probe success = blocked, want selectable")
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

// TestPoolBreakerWiring_QuotaLadderDoesNotFeedBreaker pins that plain 403
// (no reclassification) and 429 land on the per-auth quota ladder only and
// never feed the pool breaker, even past the failure threshold.
func TestPoolBreakerWiring_QuotaLadderDoesNotFeedBreaker(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth403 := registerPoolBreakerAuth(t, manager, "auth-breaker-plain-403", "claude:11", true)
	auth429 := registerPoolBreakerAuth(t, manager, "auth-breaker-429", "claude:12", true)
	peer403 := registerPoolBreakerAuth(t, manager, "auth-plain-403-peer", "claude:11", true)
	peer429 := registerPoolBreakerAuth(t, manager, "auth-429-peer", "claude:12", true)
	cleanupPoolBreakerKeys(t, "claude:11", "claude:12")

	markStatus := func(authID string, status int, code string) {
		t.Helper()
		manager.MarkResult(context.Background(), Result{
			AuthID:   authID,
			Provider: "claude",
			Model:    "claude-sonnet-4-5",
			Success:  false,
			Error: &Error{
				Code:       code,
				Message:    "rejected",
				HTTPStatus: status,
			},
		})
	}
	for i := 0; i < poolBreakerFailureThreshold+4; i++ {
		markStatus(auth403.ID, http.StatusForbidden, "forbidden")
		markStatus(auth429.ID, http.StatusTooManyRequests, "rate_limited")
	}
	for _, record := range PoolBreakerSnapshot() {
		if record.PoolKey == "claude:11" || record.PoolKey == "claude:12" {
			t.Fatalf("PoolBreakerSnapshot() contains %s after quota-ladder failures: %+v", record.PoolKey, record)
		}
	}
	if blocked, _, _ := isAuthBlockedForModel(peer403, "claude-sonnet-4-5", time.Now()); blocked {
		t.Fatalf("isAuthBlockedForModel(claude:11 peer) = blocked, want selectable (breaker not fed by plain 403s)")
	}
	if blocked, _, _ := isAuthBlockedForModel(peer429, "claude-sonnet-4-5", time.Now()); blocked {
		t.Fatalf("isAuthBlockedForModel(claude:12 peer) = blocked, want selectable (breaker not fed by 429s)")
	}
}
