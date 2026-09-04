package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// This file covers the fixes for the 2026-09-04 adversarial review of
// c5a17c60 (four confirmed HIGH findings). Each test is named after the
// finding it closes and, where the review specified an exact probe recipe,
// follows that recipe.

// --- Finding 1: cross-model cooldown contamination -------------------------

// TestEffectiveDeadline_SiblingIsolation is the permanent regression control
// for finding 1: an aggregate-only auth deadline (Quota.Reason=="quota",
// copied up from a DIFFERENT model by updateAggregatedAvailability) must not
// apply to a clean sibling model, while a GENUINE credential-wide deadline
// (CredentialCooldown / Quota.Reason=="credential_quota") must still apply
// to every model. This is the same shape of probe the review ran against
// 8a0d987d^ (which passed) and c5a17c60 (which failed) - kept in-repo so the
// regression cannot silently return.
func TestEffectiveDeadline_SiblingIsolation(t *testing.T) {
	now := time.Now()
	future := func(d time.Duration) time.Time { return now.Add(d) }

	t.Run("aggregate-only auth deadline from a sibling model does not leak", func(t *testing.T) {
		a := &Auth{
			// Derived by updateAggregatedAvailability from model A's 2h
			// quota block - NOT a genuine credential-wide failure.
			NextRetryAfter: future(2 * time.Hour),
			Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: future(2 * time.Hour)},
			ModelStates: map[string]*ModelState{
				"model-a": {Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: future(2 * time.Hour)}},
				"model-b": {}, // clean sibling
			},
		}
		got := effectiveDeadline(a, "model-b", now)
		if !got.IsZero() {
			t.Fatalf("effectiveDeadline(model-b) = %v, want zero (aggregate-only deadline must not contaminate a clean sibling)", got)
		}
	})

	t.Run("genuine CredentialCooldown deadline still applies to every model", func(t *testing.T) {
		a := &Auth{
			CredentialCooldown: true,
			NextRetryAfter:     future(30 * time.Minute),
			ModelStates: map[string]*ModelState{
				"model-b": {},
			},
		}
		got := effectiveDeadline(a, "model-b", now)
		if !got.Equal(future(30 * time.Minute)) {
			t.Fatalf("effectiveDeadline(model-b) = %v, want %v (genuine credential-wide deadline must apply to every model)", got, future(30*time.Minute))
		}
	})

	t.Run("genuine credential_quota deadline still applies to every model", func(t *testing.T) {
		a := &Auth{
			Quota: QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: future(45 * time.Minute)},
			ModelStates: map[string]*ModelState{
				"model-b": {},
			},
		}
		got := effectiveDeadline(a, "model-b", now)
		if !got.Equal(future(45 * time.Minute)) {
			t.Fatalf("effectiveDeadline(model-b) = %v, want %v (genuine credential_quota deadline must apply to every model)", got, future(45*time.Minute))
		}
	})

	t.Run("credential-level query (modelKey empty) still sees the aggregate unconditionally", func(t *testing.T) {
		a := &Auth{
			NextRetryAfter: future(2 * time.Hour),
			Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: future(2 * time.Hour)},
		}
		got := effectiveDeadline(a, "", now)
		if !got.Equal(future(2 * time.Hour)) {
			t.Fatalf("effectiveDeadline(\"\") = %v, want %v (credential-level query wants the aggregate too)", got, future(2*time.Hour))
		}
	})
}

// TestManagerMarkResult_SiblingModelNotContaminatedByAggregate is an
// end-to-end version of the same probe the review ran against MarkResult:
// model A gets a 2h quota cooldown, model B is clean, then an ordinary 404
// on B must compute B's own short backoff rather than inheriting A's 2h
// aggregate deadline.
func TestManagerMarkResult_SiblingModelNotContaminatedByAggregate(t *testing.T) {
	previousGlobal := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousGlobal) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-sibling", Provider: "claude", Status: StatusActive}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	longQuota := 3 * time.Hour
	m.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   auth.Provider,
		Model:      "model-a",
		RetryAfter: &longQuota,
		Error:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "model-b",
		Error:    &Error{HTTPStatus: http.StatusNotFound, Message: "model not found: model-b"},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil || updated.ModelStates["model-b"] == nil || updated.ModelStates["model-a"] == nil {
		t.Fatalf("model states missing: %#v", updated)
	}
	now := time.Now()
	// auth.Quota is now the aggregate over BOTH models (dominated by model-a's
	// long quota cooldown); the bug this test guards is effectiveBlock/
	// effectiveDeadline for model-b picking that aggregate up as if it were a
	// genuine credential-wide deadline. model-b's own effective deadline must
	// equal its own state's deadline, not the (much longer) aggregate.
	ownDeadline := effectiveDeadline(updated, "model-b", now)
	bState := updated.ModelStates["model-b"]
	wantOwn := laterFutureDeadline(now, bState.NextRetryAfter, bState.Quota.NextRecoverAt)
	if !ownDeadline.Equal(wantOwn) {
		t.Fatalf("effectiveDeadline(model-b) = %v, want model-b's own deadline %v (must not inherit model-a's aggregate)", ownDeadline, wantOwn)
	}
	if updated.Quota.Reason != "quota" {
		t.Fatalf("auth.Quota.Reason = %q, want aggregate-only %q (test premise check)", updated.Quota.Reason, "quota")
	}
	if !updated.Quota.NextRecoverAt.After(wantOwn) {
		t.Fatalf("test premise not met: aggregate NextRecoverAt %v must be later than model-b's own deadline %v", updated.Quota.NextRecoverAt, wantOwn)
	}
}

// --- Finding 2: single-field genuine-credential gates missed the other field ---

// TestUpdateAggregatedAvailability_UnifiesGenuineScopeFields: CredentialCooldown
// is true, NextRetryAfter has independently expired, but Quota.NextRecoverAt
// (Reason=="credential_quota") is still live 2h out. The old code checked
// only NextRetryAfter and cleared the credential-wide block early.
func TestUpdateAggregatedAvailability_UnifiesGenuineScopeFields(t *testing.T) {
	now := time.Now()
	a := &Auth{
		CredentialCooldown: true,
		NextRetryAfter:     now.Add(-time.Minute), // expired
		Quota:              QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(2 * time.Hour)},
	}
	updateAggregatedAvailability(a, now)
	if !a.Unavailable {
		t.Fatalf("Unavailable = false, want true (live Quota.NextRecoverAt must keep the credential blocked)")
	}
	blocked, _, until := effectiveBlock(a, "", now)
	if !blocked || !until.Equal(a.Quota.NextRecoverAt) {
		t.Fatalf("effectiveBlock = (%v,_,%v), want blocked until %v", blocked, until, a.Quota.NextRecoverAt)
	}
}

// TestManagerUpdate_RefreshPreservesLiveQuotaEvenWhenNextRetryAfterExpired
// is the :187 refresh case: a routine Update (e.g. after an access-token
// refresh) with a clean incoming Auth must not drop a live
// Quota.NextRecoverAt just because CredentialCooldown/NextRetryAfter alone
// looked expired.
func TestManagerUpdate_RefreshPreservesLiveQuotaEvenWhenNextRetryAfterExpired(t *testing.T) {
	m := NewManager(nil, nil, nil)
	now := time.Now()

	existing := &Auth{
		ID:                 "auth-refresh",
		Provider:           "claude",
		Status:             StatusActive,
		CredentialCooldown: true,
		Unavailable:        true,
		NextRetryAfter:     now.Add(-time.Minute), // expired
		Quota:              QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(2 * time.Hour)},
	}
	if _, err := m.Register(context.Background(), existing); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	// A clean refreshed Auth for the SAME credential (same ID, no rotated
	// refresh/access token attributes => sameAuthCredential is expected true).
	if _, err := m.Update(context.Background(), &Auth{
		ID:       "auth-refresh",
		Provider: "claude",
		Status:   StatusActive,
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	updated, ok := m.GetByID("auth-refresh")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if !updated.Quota.NextRecoverAt.Equal(existing.Quota.NextRecoverAt) || !updated.Quota.Exceeded {
		t.Fatalf("Quota = %+v, want preserved live credential_quota cooldown %+v", updated.Quota, existing.Quota)
	}
	if !updated.Unavailable {
		t.Fatalf("Unavailable = false, want true (live Quota.NextRecoverAt must keep the refreshed auth blocked)")
	}
}

// --- Finding 3: writers destroying the only longer deadline ----------------

// TestManagerMarkResult_WriterOrderRecipes reproduces the three orderings
// the review confirmed shortened a deadline that should have been preserved.
func TestManagerMarkResult_WriterOrderRecipes(t *testing.T) {
	previousGlobal := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousGlobal) })

	t.Run("model 401 then transient 500 does not shorten the 401 deadline", func(t *testing.T) {
		m := NewManager(nil, nil, nil)
		auth := &Auth{ID: "auth-recipe-1", Provider: "claude", Status: StatusActive}
		if _, err := m.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
		m.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: auth.Provider, Model: "m", Error: &Error{HTTPStatus: http.StatusUnauthorized}})
		updated, _ := m.GetByID(auth.ID)
		firstDeadline := updated.ModelStates["m"].NextRetryAfter
		if firstDeadline.IsZero() {
			t.Fatalf("expected a cooldown after 401")
		}
		m.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: auth.Provider, Model: "m", Error: &Error{HTTPStatus: http.StatusInternalServerError}})
		updated, _ = m.GetByID(auth.ID)
		secondDeadline := updated.ModelStates["m"].NextRetryAfter
		if secondDeadline.Before(firstDeadline) {
			t.Fatalf("transient 500 shortened the deadline: %v -> %v", firstDeadline, secondDeadline)
		}
	})

	t.Run("model 401 then short 429 does not shorten the 401 deadline", func(t *testing.T) {
		m := NewManager(nil, nil, nil)
		auth := &Auth{ID: "auth-recipe-2", Provider: "claude", Status: StatusActive}
		if _, err := m.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
		m.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: auth.Provider, Model: "m", Error: &Error{HTTPStatus: http.StatusUnauthorized}})
		updated, _ := m.GetByID(auth.ID)
		firstDeadline := updated.ModelStates["m"].NextRetryAfter
		if firstDeadline.IsZero() {
			t.Fatalf("expected a cooldown after 401")
		}
		short := 10 * time.Second
		m.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: auth.Provider, Model: "m", RetryAfter: &short, Error: &Error{HTTPStatus: http.StatusTooManyRequests}})
		updated, _ = m.GetByID(auth.ID)
		secondDeadline := updated.ModelStates["m"].NextRetryAfter
		if secondDeadline.Before(firstDeadline) {
			t.Fatalf("short 429 shortened the deadline: %v -> %v", firstDeadline, secondDeadline)
		}
	})

	t.Run("credential-scope 429 on model A does not shorten sibling model B's longer 401", func(t *testing.T) {
		m := NewManager(nil, nil, nil)
		auth := &Auth{ID: "auth-recipe-3", Provider: "claude", Status: StatusActive}
		if _, err := m.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
		m.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: auth.Provider, Model: "model-b", Error: &Error{HTTPStatus: http.StatusUnauthorized}})
		updated, _ := m.GetByID(auth.ID)
		bDeadline := updated.ModelStates["model-b"].NextRetryAfter
		if bDeadline.IsZero() {
			t.Fatalf("expected a cooldown for model-b after 401")
		}
		short := 10 * time.Second
		m.MarkResult(context.Background(), Result{
			AuthID: auth.ID, Provider: auth.Provider, Model: "model-a",
			RetryAfter:      &short,
			CredentialScope: true,
			Error:           &Error{HTTPStatus: http.StatusTooManyRequests},
		})
		updated, _ = m.GetByID(auth.ID)
		bAfter := updated.ModelStates["model-b"].NextRetryAfter
		if bAfter.Before(bDeadline) {
			t.Fatalf("credential-scope 429 on model-a shortened sibling model-b's deadline: %v -> %v", bDeadline, bAfter)
		}
	})
}

// --- Finding 4: registry projection false-available path -------------------

// TestClientModelProjectionForAuth_GenuineCredentialCooldownSuspendsModel is
// the projection case: a credential-wide 401 with model A otherwise clean
// must report Suspended=true, not the false-available result the direct
// field reads produced.
func TestClientModelProjectionForAuth_GenuineCredentialCooldownSuspendsModel(t *testing.T) {
	now := time.Now()
	m := &Manager{}
	auth := &Auth{
		CredentialCooldown: true,
		Unavailable:        true,
		NextRetryAfter:     now.Add(30 * time.Minute),
		ModelStates: map[string]*ModelState{
			"model-a": {Status: StatusActive},
		},
	}
	projection := m.clientModelProjectionForAuth(auth, "model-a", now)
	if !projection.Suspended {
		t.Fatalf("Suspended = false, want true (genuine credential-wide cooldown must suspend every model)")
	}
}

// --- Fifth site: hasModelError missing a live Quota.NextRecoverAt ----------

// TestHasModelError_LiveQuotaWithNilLastError answers the reviewer's open
// question about line 1571 (hasModelError): a ModelState can have
// LastError == nil while a longer Quota.NextRecoverAt remains live, and
// hasModelError must report true for it.
func TestHasModelError_LiveQuotaWithNilLastError(t *testing.T) {
	now := time.Now()
	a := &Auth{
		ModelStates: map[string]*ModelState{
			"m": {
				Status:         StatusError,
				Unavailable:    true,
				LastError:      nil,
				NextRetryAfter: now.Add(-time.Minute), // expired
				Quota:          QuotaState{Exceeded: true, NextRecoverAt: now.Add(time.Hour)},
			},
		},
	}
	if !hasModelError(a, now) {
		t.Fatalf("hasModelError = false, want true (live Quota.NextRecoverAt with nil LastError must still count)")
	}
}
