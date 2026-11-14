package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestMarkResult_ForceCredentialQuotaDoesNotInheritOrdinaryLongCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	ctx := context.Background()
	cred := &Auth{
		ID:       "auth-force-vs-ordinary-429",
		Provider: "meta",
		Status:   StatusActive,
	}
	if _, errReg := m.Register(ctx, cred); errReg != nil {
		t.Fatalf("Register(): %v", errReg)
	}

	// 1. Record an ordinary 1h credential-scoped 429.
	ordinaryRetry := time.Hour
	m.MarkResult(ctx, Result{
		AuthID:          cred.ID,
		Provider:        "meta",
		Success:         false,
		CredentialScope: true,
		RetryAfter:      &ordinaryRetry,
		Error:           &Error{HTTPStatus: http.StatusTooManyRequests, Message: "ordinary 1h quota"},
	})

	// 2. Record a 1m stop-and-cooldown (ErrorCodeForceCooldown) 429 on top of it.
	beforeForce := time.Now()
	forceRetry := time.Minute
	m.MarkResult(ctx, Result{
		AuthID:          cred.ID,
		Provider:        "meta",
		Success:         false,
		CredentialScope: true,
		RetryAfter:      &forceRetry,
		Error:           &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests, Message: "force 1m quota"},
	})

	mid, _ := m.GetByID(cred.ID)
	if mid == nil || mid.credQuotaForceUntil.After(beforeForce.Add(2*time.Minute)) {
		t.Fatalf("credQuotaForceUntil inherited ordinary 1h cooldown: %v", mid.credQuotaForceUntil)
	}

	// 3. Toggling disable-cooling must clear the ordinary 1h window and clamp down to the ~1m force deadline.
	m.SetConfig(&internalconfig.Config{DisableCooling: true})
	afterDisable, _ := m.GetByID(cred.ID)
	if afterDisable == nil {
		t.Fatal("credential missing after SetConfig")
	}
	if afterDisable.Quota.NextRecoverAt.After(beforeForce.Add(2*time.Minute)) || afterDisable.NextRetryAfter.After(beforeForce.Add(2*time.Minute)) {
		t.Fatalf("disable-cooling failed to clamp ordinary 1h window down to 1m force deadline: quota=%+v nextRetry=%v",
			afterDisable.Quota, afterDisable.NextRetryAfter)
	}
	if !afterDisable.Quota.Exceeded || !afterDisable.Quota.NextRecoverAt.After(beforeForce.Add(30*time.Second)) {
		t.Fatalf("expected 1m force quota deadline to remain active, got quota=%+v", afterDisable.Quota)
	}
}

func TestMarkResult_Non429CredentialScopeBlocksStatelessAndStatefulModels(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	ctx := context.Background()
	cred := &Auth{
		ID:       "auth-non429-cred-scope",
		Provider: "meta",
		Status:   StatusActive,
	}
	registry.GetGlobalRegistry().RegisterClient(cred.ID, "meta", []*registry.ModelInfo{
		{ID: "model-a"},
		{ID: "model-b"},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(cred.ID)
	})
	if _, errReg := m.Register(ctx, cred); errReg != nil {
		t.Fatalf("Register(): %v", errReg)
	}

	// Create ModelStates entry for model-a only, leaving model-b stateless.
	m.MarkResult(ctx, Result{
		AuthID:            cred.ID,
		Provider:          "meta",
		Model:             "model-a",
		Success:           true,
		CompletedResponse: true,
	})

	retry := 2 * time.Minute
	m.MarkResult(ctx, Result{
		AuthID:          cred.ID,
		Provider:        "meta",
		Success:         false,
		CredentialScope: true,
		RetryAfter:      &retry,
		Error:           &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "upstream 503"},
	})

	snap, _ := m.GetByID(cred.ID)
	now := time.Now()
	if IsAuthAvailableForModel(snap, "model-a", now) {
		t.Fatalf("expected model-a (with ModelState) to be blocked")
	}
	if IsAuthAvailableForModel(snap, "model-b", now) {
		t.Fatalf("expected model-b (without ModelState) to be blocked")
	}

	// Toggling disable-cooling clears non-force credentialRetryAfter.
	m.SetConfig(&internalconfig.Config{DisableCooling: true})
	cleared, _ := m.GetByID(cred.ID)
	if !IsAuthAvailableForModel(cleared, "model-a", now) || !IsAuthAvailableForModel(cleared, "model-b", now) {
		t.Fatalf("expected disable-cooling to clear ordinary credentialRetryAfter: %+v", cleared)
	}
}

func TestMarkResult_Non429CredentialScopeDoesNotInheritModelLongCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	ctx := context.Background()
	cred := &Auth{
		ID:       "auth-model-long-vs-cred-short-503",
		Provider: "meta",
		Status:   StatusActive,
	}
	if _, errReg := m.Register(ctx, cred); errReg != nil {
		t.Fatalf("Register(): %v", errReg)
	}

	// 1. model-a suffers a 1h model-level 429 (aggregating auth.NextRetryAfter to 1h).
	longRetry := time.Hour
	m.MarkResult(ctx, Result{
		AuthID:     cred.ID,
		Provider:   "meta",
		Model:      "model-a",
		Success:    false,
		RetryAfter: &longRetry,
		Error:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "model-a 1h quota"},
	})

	// 2. Credential-scoped 503 with 1m RetryAfter arrives.
	before503 := time.Now()
	shortRetry := time.Minute
	m.MarkResult(ctx, Result{
		AuthID:          cred.ID,
		Provider:        "meta",
		Success:         false,
		CredentialScope: true,
		RetryAfter:      &shortRetry,
		Error:           &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "1m 503"},
	})

	snap, _ := m.GetByID(cred.ID)
	if snap == nil {
		t.Fatal("credential missing")
	}
	if snap.credentialRetryAfter.After(before503.Add(2 * time.Minute)) {
		t.Fatalf("credentialRetryAfter inherited model-a 1h cooldown: %v", snap.credentialRetryAfter)
	}
	// At +30s: both model-a and model-b are blocked.
	if IsAuthAvailableForModel(snap, "model-a", before503.Add(30*time.Second)) || IsAuthAvailableForModel(snap, "model-b", before503.Add(30*time.Second)) {
		t.Fatalf("expected both models blocked at +30s")
	}
	// At +2m: credential-level 1m 503 has expired so model-b is available, while model-a remains blocked for 1h.
	if !IsAuthAvailableForModel(snap, "model-b", before503.Add(2*time.Minute)) {
		t.Fatalf("expected model-b available at +2m after 1m 503 expired; snap=%+v", snap)
	}
	if IsAuthAvailableForModel(snap, "model-a", before503.Add(2*time.Minute)) {
		t.Fatalf("expected model-a to remain blocked for 1h at +2m")
	}
}

func TestReconcileDisableCooling_ClearsCredentialRetryAfterInEarlyReturnBranch(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	ctx := context.Background()
	cred := &Auth{
		ID:       "auth-early-return-disable-cooling",
		Provider: "meta",
		Status:   StatusActive,
	}
	if _, errReg := m.Register(ctx, cred); errReg != nil {
		t.Fatalf("Register(): %v", errReg)
	}

	// No ModelStates: ordinary 1h 503 sets credentialRetryAfter, followed by 1m force 429 (credQuotaForceUntil).
	long503 := time.Hour
	m.MarkResult(ctx, Result{
		AuthID:          cred.ID,
		Provider:        "meta",
		Success:         false,
		CredentialScope: true,
		RetryAfter:      &long503,
		Error:           &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "ordinary 1h 503"},
	})
	beforeForce := time.Now()
	shortForce := time.Minute
	m.MarkResult(ctx, Result{
		AuthID:          cred.ID,
		Provider:        "meta",
		Success:         false,
		CredentialScope: true,
		RetryAfter:      &shortForce,
		Error:           &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests, Message: "force 1m 429"},
	})

	// Disable cooling: early-return branch (len(ModelStates) == 0 && keepCredQuotaForce) must clear ordinary credentialRetryAfter.
	m.SetConfig(&internalconfig.Config{DisableCooling: true})
	snap, _ := m.GetByID(cred.ID)
	if snap == nil {
		t.Fatal("credential missing")
	}
	if !snap.credentialRetryAfter.IsZero() {
		t.Fatalf("expected credentialRetryAfter to be cleared by DisableCooling, got %v", snap.credentialRetryAfter)
	}
	// At +30s: still blocked by 1m force quota. At +2m: force expired, credential must be selectable again.
	if IsAuthAvailableForModel(snap, "m1", beforeForce.Add(30*time.Second)) {
		t.Fatalf("expected credential blocked by 1m force quota at +30s")
	}
	if !IsAuthAvailableForModel(snap, "m1", beforeForce.Add(2*time.Minute)) {
		t.Fatalf("expected credential available at +2m once 1m force expired, got snap=%+v", snap)
	}
}

func TestCooldownStore_PersistsAndRestoresCredentialRetryAfterWithMixedModelStates(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	cdsDir := t.TempDir()
	ctx := context.Background()

	m1 := NewManager(nil, nil, nil)
	m1.SetCooldownStateStore(NewFileCooldownStateStore(cdsDir))
	cred1 := &Auth{ID: "auth-cds-mixed-cred-retry", Provider: "meta", Status: StatusActive}
	if _, errReg := m1.Register(WithSkipPersist(ctx), cred1); errReg != nil {
		t.Fatalf("Register m1: %v", errReg)
	}

	// Record a 2m model-a cooldown AND a 10m credential-scoped 503.
	before := time.Now()
	modelRetry := 2 * time.Minute
	m1.MarkResult(ctx, Result{
		AuthID:     cred1.ID,
		Provider:   "meta",
		Model:      "model-a",
		Success:    false,
		RetryAfter: &modelRetry,
		Error:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "model-a 2m"},
	})
	credRetry := 10 * time.Minute
	m1.MarkResult(ctx, Result{
		AuthID:          cred1.ID,
		Provider:        "meta",
		Success:         false,
		CredentialScope: true,
		RetryAfter:      &credRetry,
		Error:           &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "cred 10m 503"},
	})
	m1.PersistCooldownStates(ctx)

	// Restore into a fresh Manager from the persisted .cds file.
	m2 := NewManager(nil, nil, nil)
	m2.SetCooldownStateStore(NewFileCooldownStateStore(cdsDir))
	cred2 := &Auth{ID: cred1.ID, Provider: "meta", Status: StatusActive}
	if _, errReg := m2.Register(WithSkipPersist(ctx), cred2); errReg != nil {
		t.Fatalf("Register m2: %v", errReg)
	}
	if errRestore := m2.RestoreCooldownStates(ctx); errRestore != nil {
		t.Fatalf("RestoreCooldownStates: %v", errRestore)
	}

	restored, _ := m2.GetByID(cred1.ID)
	if restored == nil {
		t.Fatal("restored credential missing")
	}
	// At +5m (after model-a's 2m expired, while 10m credentialRetryAfter is still active):
	// both model-a and stateless model-b must remain blocked.
	checkAt := before.Add(5 * time.Minute)
	if IsAuthAvailableForModel(restored, "model-a", checkAt) {
		t.Fatalf("expected model-a to remain blocked at +5m by restored credentialRetryAfter: %+v", restored)
	}
	if IsAuthAvailableForModel(restored, "model-b", checkAt) {
		t.Fatalf("expected model-b to remain blocked at +5m by restored credentialRetryAfter: %+v", restored)
	}
	if !IsAuthAvailableForModel(restored, "model-a", before.Add(11*time.Minute)) || !IsAuthAvailableForModel(restored, "model-b", before.Add(11*time.Minute)) {
		t.Fatalf("expected both models available at +11m after credentialRetryAfter expired")
	}
}

func TestMarkResult_CredentialScoped429DoesNotInheritModelLongCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	ctx := context.Background()
	cred := &Auth{
		ID:       "auth-model-1h-vs-cred-1m-429",
		Provider: "meta",
		Status:   StatusActive,
	}
	if _, errReg := m.Register(ctx, cred); errReg != nil {
		t.Fatalf("Register(): %v", errReg)
	}

	// 1. model-a suffers a 1h model-level 429 (aggregating auth.Quota to 1h with reason "quota").
	longRetry := time.Hour
	m.MarkResult(ctx, Result{
		AuthID:     cred.ID,
		Provider:   "meta",
		Model:      "model-a",
		Success:    false,
		RetryAfter: &longRetry,
		Error:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "model-a 1h quota"},
	})

	// 2. Credential-scoped 429 with 1m RetryAfter arrives.
	before429 := time.Now()
	shortRetry := time.Minute
	m.MarkResult(ctx, Result{
		AuthID:          cred.ID,
		Provider:        "meta",
		Success:         false,
		CredentialScope: true,
		RetryAfter:      &shortRetry,
		Error:           &Error{HTTPStatus: http.StatusTooManyRequests, Message: "cred 1m quota"},
	})

	snap, _ := m.GetByID(cred.ID)
	if snap == nil {
		t.Fatal("credential missing")
	}
	if snap.Quota.Reason != ErrorCodeCredentialQuota {
		t.Fatalf("auth.Quota.Reason = %q, want credential_quota", snap.Quota.Reason)
	}
	if snap.Quota.NextRecoverAt.After(before429.Add(2*time.Minute)) || snap.NextRetryAfter.After(before429.Add(2*time.Minute)) {
		t.Fatalf("credential-scoped 429 inherited model-a 1h cooldown: quota=%+v nextRetry=%v", snap.Quota, snap.NextRetryAfter)
	}
	// At +30s: both model-a and healthy model-b are blocked by the 1m credential_quota.
	if IsAuthAvailableForModel(snap, "model-a", before429.Add(30*time.Second)) || IsAuthAvailableForModel(snap, "model-b", before429.Add(30*time.Second)) {
		t.Fatalf("expected both models blocked at +30s")
	}
	// At +2m: 1m credential_quota has expired so healthy model-b is available, while model-a stays blocked for 1h.
	if !IsAuthAvailableForModel(snap, "model-b", before429.Add(2*time.Minute)) {
		t.Fatalf("expected model-b available at +2m after 1m credential_quota expired; snap=%+v", snap)
	}
	if IsAuthAvailableForModel(snap, "model-a", before429.Add(2*time.Minute)) {
		t.Fatalf("expected model-a to remain blocked for 1h at +2m")
	}
}
