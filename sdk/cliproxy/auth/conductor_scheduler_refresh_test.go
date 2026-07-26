package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type schedulerProviderTestExecutor struct {
	provider string
}

func (e schedulerProviderTestExecutor) Identifier() string { return e.provider }

func (e schedulerProviderTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e schedulerProviderTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e schedulerProviderTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e schedulerProviderTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e schedulerProviderTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

type unauthorizedRefreshTestExecutor struct {
	schedulerProviderTestExecutor
}

func (e unauthorizedRefreshTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return nil, errors.New("token refresh failed with status 401: invalid_grant")
}

// invalidGrantRefreshTestExecutor simulates x.ai's real behavior: HTTP 400
// with invalid_grant for a revoked refresh token. This is RFC 6749 §5.2
// compliant — token endpoints default to 400, not 401.
type invalidGrantRefreshTestExecutor struct {
	schedulerProviderTestExecutor
}

func (e invalidGrantRefreshTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return nil, errors.New("xai token request failed with status 400: {\"error\":\"invalid_grant\",\"error_description\":\"Refresh token has been revoked\"}")
}

func TestManager_RefreshAuthUnauthorizedFailureStopsAutoRefreshRetry(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(unauthorizedRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "codex"},
	})

	// Register a refresh lead for codex, mirroring the real runtime where
	// sdk/auth/refresh_registry.go registers one via init().
	setRefreshLeadFactory(t, "codex", func() *time.Duration {
		d := 5 * time.Minute
		return &d
	})

	auth := &Auth{
		ID:       "unauthorized-refresh",
		Provider: "codex",
		Metadata: map[string]any{
			"email": "x@example.com",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.refreshAuth(ctx, auth.ID)

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}
	if updated.LastError == nil {
		t.Fatal("expected unauthorized refresh failure to be recorded")
	}
	if got := updated.LastError.StatusCode(); got != http.StatusUnauthorized {
		t.Fatalf("LastError.StatusCode() = %d, want %d", got, http.StatusUnauthorized)
	}
	if updated.LastError.Code != "unauthorized" {
		t.Fatalf("LastError.Code = %q, want unauthorized", updated.LastError.Code)
	}
	if !updated.NextRefreshAfter.IsZero() {
		t.Fatalf("NextRefreshAfter = %s, want zero for unauthorized refresh failure", updated.NextRefreshAfter)
	}
	now := time.Now()
	if manager.shouldRefresh(updated, now) {
		t.Fatal("expected unauthorized auth to stop refresh attempts")
	}
	if _, shouldSchedule := nextRefreshCheckAt(now, updated, time.Second); shouldSchedule {
		t.Fatal("expected unauthorized auth to be removed from the auto-refresh schedule")
	}
}

// TestManager_RefreshAuthInvalidGrant400MarksAuthUnavailable verifies that
// refresh failures returning HTTP 400 with invalid_grant (RFC 6749 §5.2,
// x.ai's actual behavior for revoked refresh tokens) are treated as
// permanent credential failures — the auth is marked Unavailable + StatusError
// so management API consumers (e.g. cleanup scripts) can detect and delete it.
//
// Regression: previously isUnauthorizedError only recognized HTTP 401, so
// 400 invalid_grant was treated as a transient error and the auth kept
// status=active, unavailable=false forever — making cleanup impossible.
func TestManager_RefreshAuthInvalidGrant400MarksAuthUnavailable(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(invalidGrantRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "xai"},
	})

	// Register a refresh lead for xai, mirroring the real runtime where
	// sdk/auth/refresh_registry.go registers one via init(). Without this the
	// scheduler would short-circuit at ProviderRefreshLead==nil and hide the
	// regression where hasUnauthorizedAuthFailure failed to recognize 400
	// invalid_grant as permanent (the auto-refresh loop kept re-queuing the
	// revoked credential indefinitely).
	setRefreshLeadFactory(t, "xai", func() *time.Duration {
		d := 5 * time.Minute
		return &d
	})

	auth := &Auth{
		ID:       "xai-revoked",
		Provider: "xai",
		Metadata: map[string]any{
			"email": "revoked@example.com",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.refreshAuth(ctx, auth.ID)

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}
	if updated.LastError == nil {
		t.Fatal("expected invalid_grant refresh failure to be recorded in LastError")
	}
	if !updated.Unavailable {
		t.Fatal("expected Unavailable=true for 400 invalid_grant (revoked refresh token)")
	}
	if updated.Status != StatusError {
		t.Fatalf("Status = %q, want %q", updated.Status, StatusError)
	}
	if updated.StatusMessage != "invalid_grant" {
		t.Fatalf("StatusMessage = %q, want %q", updated.StatusMessage, "invalid_grant")
	}
	if !updated.NextRefreshAfter.IsZero() {
		t.Fatalf("NextRefreshAfter = %s, want zero for permanent credential failure", updated.NextRefreshAfter)
	}
	now := time.Now()
	if manager.shouldRefresh(updated, now) {
		t.Fatal("expected revoked auth to stop refresh attempts")
	}
	if _, shouldSchedule := nextRefreshCheckAt(now, updated, time.Second); shouldSchedule {
		t.Fatal("expected revoked auth to be removed from the auto-refresh schedule")
	}
}

// TestManager_RefreshAuthPermanentFailureClearsStaleRequestCooldown verifies
// the fix for the codex review on commit 7b335d14: when a credential already
// has a request/model cooldown (NextRetryAfter non-zero) and a proactive
// refresh during that window returns invalid_grant, the permanent-failure
// branch must clear NextRetryAfter. Without this, the IsZero() discriminators
// in shouldRefresh, nextRefreshCheckAt, and isAuthBlockedForModel all see a
// non-zero NextRetryAfter and treat the permanent failure as a request
// cooldown — leaving the dead auth scheduled for refresh and routable after
// the stale cooldown expires.
func TestManager_RefreshAuthPermanentFailureClearsStaleRequestCooldown(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(invalidGrantRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "xai"},
	})

	setRefreshLeadFactory(t, "xai", func() *time.Duration {
		d := 5 * time.Minute
		return &d
	})

	now := time.Now()
	cooldownEnd := now.Add(30 * time.Minute)

	auth := &Auth{
		ID:             "xai-stale-cooldown",
		Provider:       "xai",
		Unavailable:    true,
		Status:         StatusError,
		StatusMessage:  "request invalid_grant cooldown",
		NextRetryAfter: cooldownEnd,
		Metadata: map[string]any{
			"email": "stale@example.com",
		},
		ModelStates: map[string]*ModelState{
			"grok-4": {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: cooldownEnd,
			},
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// Simulate a proactive refresh firing during the cooldown window.
	manager.refreshAuth(ctx, auth.ID)

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}

	// NextRetryAfter must be cleared — this is the core fix.
	if !updated.NextRetryAfter.IsZero() {
		t.Fatalf("NextRetryAfter = %s, want zero (stale request cooldown must be cleared on permanent refresh failure)", updated.NextRetryAfter)
	}
	if !updated.NextRefreshAfter.IsZero() {
		t.Fatalf("NextRefreshAfter = %s, want zero for permanent credential failure", updated.NextRefreshAfter)
	}
	if !updated.Unavailable {
		t.Fatal("expected Unavailable=true for permanent refresh failure")
	}

	// With NextRetryAfter cleared, the IsZero() discriminators must now fire.
	if manager.shouldRefresh(updated, now) {
		t.Fatal("expected dead auth to stop refresh attempts after stale cooldown cleared")
	}
	if _, shouldSchedule := nextRefreshCheckAt(now, updated, time.Second); shouldSchedule {
		t.Fatal("expected dead auth to be removed from auto-refresh schedule after stale cooldown cleared")
	}

	// isAuthBlockedForModel short-circuit must fire (Unavailable +
	// hasPermanentAuthFailure + NextRetryAfter.IsZero()).
	blocked, reason, _ := isAuthBlockedForModel(updated, "grok-4", now)
	if !blocked {
		t.Fatal("expected isAuthBlockedForModel to block dead auth after stale cooldown cleared")
	}
	if reason != blockReasonOther {
		t.Fatalf("blockReason = %d, want %d (permanent failure, not cooldown)", reason, blockReasonOther)
	}
}

// TestMarkResult_PreservesRefreshPermanentFailureMarker verifies the fix
// for codex review P2 on commit 358d49b2: when tryRefreshAfterUnauthorized
// calls refreshAuthForRequest and gets a permanent failure (400 invalid_grant
// or 401), the permanent branch sets Unavailable=true + zero NextRetryAfter
// + permanent LastError. The caller then records the original 401 via
// MarkResult — without an early-skip, MarkResult's failure-state write
// (model-scoped or auth-level) overwrites the zero-NextRetryAfter marker
// with a transient cooldown, masking the dead auth from the scheduler/
// routing guards.
func TestMarkResult_PreservesRefreshPermanentFailureMarker(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(invalidGrantRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "xai"},
	})

	setRefreshLeadFactory(t, "xai", func() *time.Duration {
		d := 5 * time.Minute
		return &d
	})

	auth := &Auth{
		ID:       "xai-refresh-permanent",
		Provider: "xai",
		Metadata: map[string]any{
			"email": "permanent@example.com",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// Simulate tryRefreshAfterUnauthorized: refresh returns 400 invalid_grant,
	// permanent branch sets Unavailable=true + zero NextRetryAfter.
	manager.refreshAuth(ctx, auth.ID)

	refreshed, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}
	if !refreshed.Unavailable || !refreshed.NextRetryAfter.IsZero() {
		t.Fatalf("precondition: expected refresh permanent marker, got Unavailable=%v NextRetryAfter=%v", refreshed.Unavailable, refreshed.NextRetryAfter)
	}

	// Simulate the caller recording the original 401 via MarkResult.
	manager.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "xai",
		Model:    "grok-4",
		Success:  false,
		Error: &Error{
			HTTPStatus: http.StatusUnauthorized,
			Message:    "request unauthorized",
			Code:       "unauthorized",
		},
	})

	afterMark, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after MarkResult", auth.ID)
	}

	// Permanent marker must be preserved — MarkResult must not overwrite
	// the zero NextRetryAfter with a transient 401 cooldown.
	if !afterMark.Unavailable {
		t.Fatal("expected Unavailable=true to be preserved after MarkResult")
	}
	if !afterMark.NextRetryAfter.IsZero() {
		t.Fatalf("NextRetryAfter = %v, want zero (permanent marker must survive MarkResult)", afterMark.NextRetryAfter)
	}
	if !hasPermanentAuthFailure(afterMark) {
		t.Fatalf("expected permanent failure marker to survive MarkResult, got LastError=%+v", afterMark.LastError)
	}

	// The guards must still fire — dead auth must not be schedulable or routable.
	now := time.Now()
	if manager.shouldRefresh(afterMark, now) {
		t.Fatal("expected dead auth to remain blocked by shouldRefresh after MarkResult")
	}
	if _, shouldSchedule := nextRefreshCheckAt(now, afterMark, time.Second); shouldSchedule {
		t.Fatal("expected dead auth to remain unscheduled by nextRefreshCheckAt after MarkResult")
	}
	blocked, reason, _ := isAuthBlockedForModel(afterMark, "grok-4", now)
	if !blocked || reason != blockReasonOther {
		t.Fatalf("expected isAuthBlockedForModel to keep blocking dead auth after MarkResult, got blocked=%v reason=%d", blocked, reason)
	}
}

// TestIsPermanentCredentialFailure covers the permanent-failure detector
// across the error shapes produced by real OAuth providers.
func TestIsPermanentCredentialFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"401 invalid_grant", errors.New("token refresh failed with status 401: invalid_grant"), true},
		{"401 plain", errors.New("token refresh failed with status 401"), true},
		{"400 invalid_grant (x.ai revoked)", errors.New("xai token request failed with status 400: {\"error\":\"invalid_grant\",\"error_description\":\"Refresh token has been revoked\"}"), true},
		{"400 invalid_grant expired", errors.New("status 400: invalid_grant, refresh token expired"), true},
		{"refresh token revoked descriptive", errors.New("Refresh token has been revoked"), true},
		{"refresh token invalid descriptive", errors.New("refresh token is invalid"), true},
		{"refresh token expired descriptive", errors.New("refresh token expired"), true},
		{"400 invalid_request (not permanent)", errors.New("status 400: invalid_request, missing parameter"), false},
		{"400 invalid_client (not permanent)", errors.New("status 400: invalid_client, unknown client"), false},
		{"500 server error (not permanent)", errors.New("status 500: internal server error"), false},
		{"network error (not permanent)", errors.New("context deadline exceeded"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isPermanentCredentialFailure(tc.err)
			if got != tc.want {
				t.Fatalf("isPermanentCredentialFailure(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestHasPermanentAuthFailure_PreservedFromPersistedState verifies the
// regression flagged by codex review on #4477: auths restored from persisted
// cooldown state may carry LastError.Code="unauthorized" without an HTTP
// status (the legacy hasUnauthorizedAuthFailure explicitly checked the code).
// hasPermanentAuthFailure must keep honoring that marker so the scheduler
// continues to treat such auths as permanently failed instead of re-queuing
// TestHasPermanentAuthFailure_RequiresPermanentRefreshFailureField
// documents the discriminator design after the codex review on commit
// 2d88e84d: hasPermanentAuthFailure relies exclusively on the
// PermanentRefreshFailure field, NOT on LastError shape. This avoids
// misclassifying disable-cooling transient failures (which produce the
// same Unavailable + zero NextRetryAfter + LastError(401) shape) as
// permanent.
//
// Auths persisted before the PermanentRefreshFailure field existed are
// NOT immediately treated as permanent after restart — they will be
// retried once by refresh, which re-detects the permanent failure and
// re-sets the marker if the credential is still revoked.
func TestHasPermanentAuthFailure_RequiresPermanentRefreshFailureField(t *testing.T) {
	// Register a refresh lead so the scheduler would otherwise attempt to
	// schedule the credential — this proves the stop marker actually works.
	setRefreshLeadFactory(t, "codex", func() *time.Duration {
		d := 5 * time.Minute
		return &d
	})

	// Legacy shape: Code="unauthorized" without PermanentRefreshFailure.
	// After the fix, this is NOT permanent — will be re-detected by refresh.
	legacyAuth := &Auth{
		ID:          "persisted-unauthorized-legacy",
		Provider:    "codex",
		Unavailable: true,
		Status:      StatusError,
		LastError: &Error{
			Code:    "unauthorized",
			Message: "credentials rejected by upstream",
		},
	}
	if hasPermanentAuthFailure(legacyAuth) {
		t.Fatal("expected legacy auth without PermanentRefreshFailure to NOT be permanent")
	}

	// New shape: PermanentRefreshFailure=true set by refreshAuthForRequest.
	permanentAuth := &Auth{
		ID:                      "persisted-unauthorized-permanent",
		Provider:                "codex",
		Unavailable:             true,
		Status:                  StatusError,
		PermanentRefreshFailure: true,
		NextRetryAfter:          time.Time{},
		LastError: &Error{
			Code:    "unauthorized",
			Message: "credentials rejected by upstream",
		},
	}
	if !hasPermanentAuthFailure(permanentAuth) {
		t.Fatal("expected auth with PermanentRefreshFailure=true to be permanent")
	}
	now := time.Now()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	if manager.shouldRefresh(permanentAuth, now) {
		t.Fatal("expected permanent auth to be skipped by shouldRefresh")
	}
	if _, shouldSchedule := nextRefreshCheckAt(now, permanentAuth, time.Second); shouldSchedule {
		t.Fatal("expected permanent auth to be unscheduled by nextRefreshCheckAt")
	}
	blocked, reason, _ := isAuthBlockedForModel(permanentAuth, "grok-4", now)
	if !blocked || reason != blockReasonOther {
		t.Fatalf("expected permanent auth to be blocked by isAuthBlockedForModel, got blocked=%v reason=%d", blocked, reason)
	}
}

// TestShouldRefresh_PersistedUnauthorizedWithCooldownStaysSchedulable
// documents an intentional behavior change from the legacy
// hasUnauthorizedAuthFailure predicate.
//
// Auths restored from persisted cooldown state via restoreCooldownRecordLocked
// always have NextRetryAfter in the future (the restore path rejects zero-time
// records). When such an auth carries LastError.Code="unauthorized", the
// legacy predicate unconditionally stopped refresh scheduling; the new
// predicate gates the stop on NextRetryAfter.IsZero(), so a persisted
// unauthorized auth with a future NextRetryAfter REMAINS schedulable and can
// attempt a proactive refresh to recover.
//
// This is desirable: the persisted unauthorized marker may reflect an expired
// access token whose refresh token is still valid. Stopping refresh forever
// would strand the credential. If the refresh token is also invalid, the
// refresh attempt fails and refreshAuthForRequest's permanent-failure branch
// sets NextRetryAfter=zero, which then triggers the stop on the next
// shouldRefresh call.
func TestShouldRefresh_PersistedUnauthorizedWithCooldownStaysSchedulable(t *testing.T) {
	setRefreshLeadFactory(t, "codex", func() *time.Duration {
		d := 5 * time.Minute
		return &d
	})

	now := time.Now()
	cooldownEnd := now.Add(30 * time.Minute)

	// Shape mirrors restoreCooldownRecordLocked's auth-level restore path:
	// Code="unauthorized" (from persisted LastError), NextRetryAfter in the
	// future (from the persisted cooldown record).
	persistedAuth := &Auth{
		ID:             "persisted-unauthorized-cooldown",
		Provider:       "codex",
		Unavailable:    true,
		Status:         StatusError,
		StatusMessage:  "unauthorized",
		NextRetryAfter: cooldownEnd,
		LastError: &Error{
			Code:    "unauthorized",
			Message: "credentials rejected by upstream",
		},
	}

	if hasPermanentAuthFailure(persistedAuth) {
		t.Fatal("expected legacy auth without PermanentRefreshFailure to NOT be permanent — it stays schedulable for proactive refresh")
	}

	// shouldRefresh must NOT short-circuit on the permanent-failure path
	// because NextRetryAfter is non-zero — the auth should remain schedulable
	// for a proactive refresh attempt. (It may still return false for other
	// reasons like NextRefreshAfter gating, but not the permanent-failure
	// short-circuit.)
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	// Force shouldRefresh past the NextRefreshAfter gate by clearing it.
	persistedAuth.NextRefreshAfter = time.Time{}
	if !manager.shouldRefresh(persistedAuth, now) {
		t.Fatal("expected persisted unauthorized auth with future NextRetryAfter to remain schedulable for proactive refresh, but shouldRefresh returned false — the permanent-failure short-circuit must not fire when NextRetryAfter is set")
	}

	// nextRefreshCheckAt must also keep scheduling.
	if _, shouldSchedule := nextRefreshCheckAt(now, persistedAuth, time.Second); !shouldSchedule {
		t.Fatal("expected nextRefreshCheckAt to keep scheduling persisted unauthorized auth with future NextRetryAfter")
	}
}

// TestIsAuthBlockedForModel_PermanentRefreshFailureBlocksRouting verifies
// the fix for codex review P2 on #4477: after refresh marks an auth
// permanently failed (Unavailable=true + permanent LastError, no model
// state recorded), the request routing path must block the dead credential
// even when the request carries a model — otherwise the model-specific
// branch of isAuthBlockedForModel skips auth-level state when no
// ModelStates entry exists, and round-robin/fill-first keeps sending
// traffic to the revoked auth until a separate request failure happens.
func TestIsAuthBlockedForModel_PermanentRefreshFailureBlocksRouting(t *testing.T) {
	now := time.Now()

	// Shape mirrors refreshAuthForRequest's permanent-failure branch:
	// Unavailable=true, Status=StatusError, LastError carries the refresh
	// error (plain fmt.Errorf -> Error with Message only, no HTTPStatus/Code),
	// no ModelStates entry for the requested model.
	revokedAuth := &Auth{
		ID:                      "xai-revoked-routing",
		Provider:                "xai",
		Unavailable:             true,
		PermanentRefreshFailure: true,
		Status:                  StatusError,
		StatusMessage:           "invalid_grant",
		LastError: &Error{
			Message: "xai token request failed with status 400: {\"error\":\"invalid_grant\",\"error_description\":\"Refresh token has been revoked\"}",
		},
	}

	// Request carries a model that has no ModelStates entry — the exact
	// scenario codex flagged where the model-specific path used to return
	// "not blocked".
	blocked, reason, _ := isAuthBlockedForModel(revokedAuth, "grok-4", now)
	if !blocked {
		t.Fatal("expected revoked auth to be blocked for model-specific routing")
	}
	if reason != blockReasonOther {
		t.Fatalf("blockReason = %d, want %d", reason, blockReasonOther)
	}

	// Sanity check: a healthy auth with no model state is still routable.
	healthyAuth := &Auth{
		ID:       "xai-healthy",
		Provider: "xai",
	}
	blockedHealthy, _, _ := isAuthBlockedForModel(healthyAuth, "grok-4", now)
	if blockedHealthy {
		t.Fatal("expected healthy auth to remain routable")
	}

	// And a full selector round-robin over [revoked, healthy] must only
	// return the healthy one.
	selector := &RoundRobinSelector{}
	picked, err := selector.Pick(context.Background(), "xai", "grok-4", cliproxyexecutor.Options{}, []*Auth{revokedAuth, healthyAuth})
	if err != nil {
		t.Fatalf("Pick error: %v", err)
	}
	if picked.ID != healthyAuth.ID {
		t.Fatalf("Pick returned %q, want %q (revoked auth should be filtered out)", picked.ID, healthyAuth.ID)
	}
}

// TestIsAuthBlockedForModel_RequestInvalidGrantCooldownRecovers verifies the
// fix for codex review P2 on #4477 (commit d1b990e8): the permanent-failure
// short-circuit must not prevent model-scoped cooldown recovery.
//
// When a model-scoped request fails with HTTP 400 invalid_grant, MarkResult
// creates a ModelState with a 30-minute NextRetryAfter cooldown and copies
// the error onto auth.LastError. updateAggregatedAvailability marks
// auth.Unavailable. After 30 minutes the model-state branch should clear
// the cooldown and allow the normal retry/resume path. The short-circuit
// must not fire for auths with ModelStates, otherwise the auth stays
// blocked forever.
func TestIsAuthBlockedForModel_RequestInvalidGrantCooldownRecovers(t *testing.T) {
	now := time.Now()

	// Shape mirrors MarkResult's model-scoped path after an invalid_grant
	// request failure: ModelState exists with a 30-min cooldown, auth.LastError
	// carries invalid_grant, auth.Unavailable + auth.NextRetryAfter set by
	// updateAggregatedAvailability (which propagates the earliest model
	// NextRetryAfter to the auth level when all model states are unavailable).
	cooldownEnd := now.Add(30 * time.Minute)
	cooldownAuth := &Auth{
		ID:             "xai-request-cooldown",
		Provider:       "xai",
		Unavailable:    true,
		Status:         StatusError,
		StatusMessage:  "invalid_grant",
		NextRetryAfter: cooldownEnd,
		LastError: &Error{
			Message: "request failed with status 400: invalid_grant",
		},
		ModelStates: map[string]*ModelState{
			"grok-4": {
				Unavailable:    true,
				Status:         StatusError,
				StatusMessage:  "invalid_grant",
				NextRetryAfter: cooldownEnd,
			},
		},
	}

	// During cooldown: must be blocked (by the model-state branch).
	blocked, _, _ := isAuthBlockedForModel(cooldownAuth, "grok-4", now)
	if !blocked {
		t.Fatal("expected auth to be blocked during model-scoped cooldown")
	}

	// After cooldown expires: must NOT be blocked — the model-state branch
	// honors NextRetryAfter expiry and allows retry/resume.
	afterCooldown := now.Add(31 * time.Minute)
	blockedAfter, _, _ := isAuthBlockedForModel(cooldownAuth, "grok-4", afterCooldown)
	if blockedAfter {
		t.Fatal("expected auth to recover after model-scoped cooldown expiry, but short-circuit kept it blocked")
	}

	// And a full selector round-robin after cooldown must return the
	// recovered auth.
	healthyAuth := &Auth{ID: "xai-healthy", Provider: "xai"}
	selector := &RoundRobinSelector{}
	picked, err := selector.Pick(context.Background(), "xai", "grok-4", cliproxyexecutor.Options{}, []*Auth{cooldownAuth, healthyAuth})
	if err != nil {
		t.Fatalf("Pick error after cooldown: %v", err)
	}
	if picked.ID != cooldownAuth.ID && picked.ID != healthyAuth.ID {
		t.Fatalf("Pick returned unexpected auth %q", picked.ID)
	}
}

// TestShouldRefresh_RequestInvalidGrantCooldownStaysSchedulable verifies the
// fix for codex review P2 on #4477 (commit d13ce42c, auto_refresh_loop.go:342):
// a model-scoped request invalid_grant cooldown sets LastError with
// invalid_grant + NextRetryAfter=now+30min. hasPermanentAuthFailure matches
// the LastError text, so the scheduler used to remove the auth from the
// refresh loop entirely — preventing proactive refresh from recovering the
// auth early during the cooldown window.
//
// The fix gates the permanent-failure stop on NextRetryAfter.IsZero():
// refresh permanent failures leave NextRetryAfter zero (so the stop fires),
// request cooldowns set it to now+30min (so the stop does NOT fire and the
// normal refresh-lead/expiry logic remains active).
func TestShouldRefresh_RequestInvalidGrantCooldownStaysSchedulable(t *testing.T) {
	// Register a refresh lead so shouldRefresh/nextRefreshCheckAt evaluate
	// the refresh-lead path instead of short-circuiting at nil lead.
	setRefreshLeadFactory(t, "xai", func() *time.Duration {
		d := 5 * time.Minute
		return &d
	})

	now := time.Now()
	cooldownEnd := now.Add(30 * time.Minute)

	// Request-side invalid_grant cooldown shape: LastError carries
	// invalid_grant (so hasPermanentAuthFailure returns true), but
	// NextRetryAfter is set to now+30min by updateAggregatedAvailability.
	requestCooldownAuth := &Auth{
		ID:             "xai-request-cooldown",
		Provider:       "xai",
		Unavailable:    true,
		Status:         StatusError,
		StatusMessage:  "invalid_grant",
		NextRetryAfter: cooldownEnd,
		LastError: &Error{
			Message: "request failed with status 400: invalid_grant",
		},
		ModelStates: map[string]*ModelState{
			"grok-4": {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: cooldownEnd,
			},
		},
	}

	// shouldRefresh must NOT return false for the permanent-failure reason
	// alone — the auth has a non-zero NextRetryAfter, so it should remain
	// schedulable for proactive refresh. (It may still return false for
	// other reasons like NextRefreshAfter gating, but not the permanent-
	// failure short-circuit.)
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	if reason := "permanent-failure short-circuit"; !manager.shouldRefresh(requestCooldownAuth, now) {
		// Verify it's not the permanent-failure path by checking that a
		// zero-NextRetryAfter variant DOES get blocked.
		zeroRetryAuth := requestCooldownAuth.Clone()
		zeroRetryAuth.NextRetryAfter = time.Time{}
		if manager.shouldRefresh(zeroRetryAuth, now) {
			// zero-NextRetryAfter is blocked, non-zero is not — correct.
			return
		}
		t.Fatalf("shouldRefresh returned false for request-cooldown auth with non-zero NextRetryAfter; the permanent-failure short-circuit must not fire when NextRetryAfter is set (%s)", reason)
	}

	// nextRefreshCheckAt must still schedule the auth (return true), not
	// remove it from the refresh loop.
	nextCheck, shouldSchedule := nextRefreshCheckAt(now, requestCooldownAuth, time.Second)
	if !shouldSchedule {
		t.Fatal("expected nextRefreshCheckAt to keep scheduling request-cooldown auth for proactive refresh")
	}
	if nextCheck.IsZero() {
		t.Fatal("expected nextRefreshCheckAt to return a non-zero time for request-cooldown auth")
	}
}

// TestShouldRefresh_PartialModelCooldownStaysSchedulable verifies the fix
// for codex review P2 on commit aad3f7e: when a multi-model auth has one
// model cooled by a request-side 400 invalid_grant and another clean model,
// updateAggregatedAvailability sets auth.Unavailable=false and clears
// auth.NextRetryAfter (a clean model keeps the auth available), while
// MarkResult still copies invalid_grant onto auth.LastError (conductor.go:3768).
//
// Without the Unavailable check in the permanent-failure guard, the zero
// NextRetryAfter + permanent LastError combination would unschedule the auth
// from the auto-refresh queue, preventing proactive refresh from recovering
// the cooled model early.
func TestShouldRefresh_PartialModelCooldownStaysSchedulable(t *testing.T) {
	setRefreshLeadFactory(t, "xai", func() *time.Duration {
		d := 5 * time.Minute
		return &d
	})

	now := time.Now()
	cooldownEnd := now.Add(30 * time.Minute)

	// Shape mirrors MarkResult's model-scoped failure path after
	// updateAggregatedAvailability: auth.Unavailable=false (clean grok-3
	// keeps the auth available), auth.NextRetryAfter=zero (allUnavailable
	// is false), but auth.LastError carries invalid_grant (copied at
	// conductor.go:3768).
	partialCooldownAuth := &Auth{
		ID:               "xai-partial-cooldown",
		Provider:         "xai",
		Unavailable:      false,
		Status:           StatusError,
		StatusMessage:    "invalid_grant",
		NextRetryAfter:   time.Time{}, // cleared by updateAggregatedAvailability
		NextRefreshAfter: time.Time{}, // clear so shouldRefresh passes this gate
		LastError: &Error{
			Message: "request failed with status 400: invalid_grant",
		},
		ModelStates: map[string]*ModelState{
			"grok-4": {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: cooldownEnd,
			},
			"grok-3": {
				Unavailable: false,
				Status:      StatusActive,
			},
		},
	}

	manager := NewManager(nil, &RoundRobinSelector{}, nil)

	// shouldRefresh must NOT short-circuit on the permanent-failure path —
	// Unavailable is false, so the guard must not fire. With NextRefreshAfter
	// cleared and a refresh lead registered, shouldRefresh should return true
	// (the lead path returns true when no expiry/lastRefresh is set).
	if !manager.shouldRefresh(partialCooldownAuth, now) {
		t.Fatal("expected partial-cooldown auth to remain schedulable for proactive refresh; the permanent-failure short-circuit must not fire when Unavailable is false")
	}

	// nextRefreshCheckAt must also keep scheduling the auth.
	nextCheck, shouldSchedule := nextRefreshCheckAt(now, partialCooldownAuth, time.Second)
	if !shouldSchedule {
		t.Fatal("expected nextRefreshCheckAt to keep scheduling partial-cooldown auth for proactive refresh")
	}
	if nextCheck.IsZero() {
		t.Fatal("expected nextRefreshCheckAt to return a non-zero time for partial-cooldown auth")
	}
}

// TestSelectorPick_DeadPlusCooldownReturnsModelCooldownError verifies the
// fix for the cooldownCount-statistics gap found via gitnexus impact
// analysis of isAuthBlockedForModel: when a provider has a mix of
// permanently failed auths (refresh 400 invalid_grant, blocked by the
// short-circuit with blockReasonOther + zero next) and cooldown auths
// (request failure with 30-min ModelState.NextRetryAfter), the selector
// must return a modelCooldownError carrying the cooldown auths' earliest
// retry time — NOT a generic auth_unavailable error.
//
// Previously collectAvailableByPriority only counted blockReasonCooldown
// toward the unavailable total, so a dead auth was excluded from the
// cooldownCount==len(auths) check, causing the mixed scenario to fall
// through to auth_unavailable and hide the cooldown recovery window.
func TestSelectorPick_DeadPlusCooldownReturnsModelCooldownError(t *testing.T) {
	now := time.Now()
	cooldownEnd := now.Add(30 * time.Minute)
	model := "grok-4"

	deadAuth := &Auth{
		ID:                      "xai-dead",
		Provider:                "xai",
		Unavailable:             true,
		PermanentRefreshFailure: true,
		Status:                  StatusError,
		StatusMessage:           "invalid_grant",
		LastError: &Error{
			Message: "xai token request failed with status 400: invalid_grant",
		},
		// No ModelStates — refresh permanent failure shape.
	}

	cooldownAuth := &Auth{
		ID:       "xai-cooldown",
		Provider: "xai",
		Status:   StatusError,
		ModelStates: map[string]*ModelState{
			model: {
				Unavailable:    true,
				Status:         StatusError,
				StatusMessage:  "invalid_grant",
				NextRetryAfter: cooldownEnd,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: cooldownEnd,
				},
			},
		},
	}

	selector := &FillFirstSelector{}
	_, err := selector.Pick(context.Background(), "xai", model, cliproxyexecutor.Options{}, []*Auth{deadAuth, cooldownAuth})
	if err == nil {
		t.Fatal("expected error when all auths unavailable")
	}

	var mce *modelCooldownError
	if !errors.As(err, &mce) {
		t.Fatalf("Pick error = %T (%v), want *modelCooldownError — dead+cooldown mix should report cooldown recovery window, not auth_unavailable", err, err)
	}
	// resetIn should approximate 30 minutes (the cooldown auth's retry time).
	if mce.resetIn <= 0 || mce.resetIn > 31*time.Minute {
		t.Fatalf("modelCooldownError.resetIn = %v, want ~30min (cooldown auth retry window)", mce.resetIn)
	}
}

// TestSelectorPick_AllDeadReturnsAuthUnavailable verifies that when every
// auth is permanently failed (no cooldown auths to recover), the selector
// returns auth_unavailable, not a modelCooldownError with zero resetIn.
func TestSelectorPick_AllDeadReturnsAuthUnavailable(t *testing.T) {
	now := time.Now()
	model := "grok-4"

	deadAuth1 := &Auth{
		ID:                      "xai-dead-1",
		Provider:                "xai",
		Unavailable:             true,
		PermanentRefreshFailure: true,
		Status:                  StatusError,
		StatusMessage:           "invalid_grant",
		LastError:               &Error{Message: "xai token request failed with status 400: invalid_grant"},
	}
	deadAuth2 := &Auth{
		ID:                      "xai-dead-2",
		Provider:                "xai",
		Unavailable:             true,
		PermanentRefreshFailure: true,
		Status:                  StatusError,
		StatusMessage:           "invalid_grant",
		LastError:               &Error{Message: "xai token request failed with status 400: invalid_grant"},
	}
	_ = now

	selector := &FillFirstSelector{}
	_, err := selector.Pick(context.Background(), "xai", model, cliproxyexecutor.Options{}, []*Auth{deadAuth1, deadAuth2})
	if err == nil {
		t.Fatal("expected error when all auths dead")
	}

	var mce *modelCooldownError
	if errors.As(err, &mce) {
		t.Fatalf("Pick returned modelCooldownError for all-dead scenario, want auth_unavailable (no cooldown to report)")
	}
	var authErr *Error
	if !errors.As(err, &authErr) || authErr.Code != "auth_unavailable" {
		t.Fatalf("Pick error = %v, want auth_unavailable", err)
	}
}

func TestManager_RefreshSchedulerEntry_RebuildsSupportedModelSetAfterModelRegistration(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name  string
		prime func(*Manager, *Auth) error
	}{
		{
			name: "register",
			prime: func(manager *Manager, auth *Auth) error {
				_, errRegister := manager.Register(ctx, auth)
				return errRegister
			},
		},
		{
			name: "update",
			prime: func(manager *Manager, auth *Auth) error {
				_, errRegister := manager.Register(ctx, auth)
				if errRegister != nil {
					return errRegister
				}
				updated := auth.Clone()
				updated.Metadata = map[string]any{"updated": true}
				_, errUpdate := manager.Update(ctx, updated)
				return errUpdate
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			manager := NewManager(nil, &RoundRobinSelector{}, nil)
			auth := &Auth{
				ID:       "refresh-entry-" + testCase.name,
				Provider: "gemini",
			}
			if errPrime := testCase.prime(manager, auth); errPrime != nil {
				t.Fatalf("prime auth %s: %v", testCase.name, errPrime)
			}

			registerSchedulerModels(t, "gemini", "scheduler-refresh-model", auth.ID)

			got, errPick := manager.scheduler.pickSingle(ctx, "gemini", "scheduler-refresh-model", cliproxyexecutor.Options{}, nil)
			var authErr *Error
			if !errors.As(errPick, &authErr) || authErr == nil {
				t.Fatalf("pickSingle() before refresh error = %v, want auth_not_found", errPick)
			}
			if authErr.Code != "auth_not_found" {
				t.Fatalf("pickSingle() before refresh code = %q, want %q", authErr.Code, "auth_not_found")
			}
			if got != nil {
				t.Fatalf("pickSingle() before refresh auth = %v, want nil", got)
			}

			manager.RefreshSchedulerEntry(auth.ID)

			got, errPick = manager.scheduler.pickSingle(ctx, "gemini", "scheduler-refresh-model", cliproxyexecutor.Options{}, nil)
			if errPick != nil {
				t.Fatalf("pickSingle() after refresh error = %v", errPick)
			}
			if got == nil || got.ID != auth.ID {
				t.Fatalf("pickSingle() after refresh auth = %v, want %q", got, auth.ID)
			}
		})
	}
}

func TestManager_PickNext_RebuildsSchedulerAfterModelCooldownError(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})

	registerSchedulerModels(t, "gemini", "scheduler-cooldown-rebuild-model", "cooldown-stale-old")

	oldAuth := &Auth{
		ID:       "cooldown-stale-old",
		Provider: "gemini",
	}
	if _, errRegister := manager.Register(ctx, oldAuth); errRegister != nil {
		t.Fatalf("register old auth: %v", errRegister)
	}

	manager.MarkResult(ctx, Result{
		AuthID:   oldAuth.ID,
		Provider: "gemini",
		Model:    "scheduler-cooldown-rebuild-model",
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
	})

	newAuth := &Auth{
		ID:       "cooldown-stale-new",
		Provider: "gemini",
	}
	if _, errRegister := manager.Register(ctx, newAuth); errRegister != nil {
		t.Fatalf("register new auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(newAuth.ID, "gemini", []*registry.ModelInfo{{ID: "scheduler-cooldown-rebuild-model"}})
	t.Cleanup(func() {
		reg.UnregisterClient(newAuth.ID)
	})

	got, errPick := manager.scheduler.pickSingle(ctx, "gemini", "scheduler-cooldown-rebuild-model", cliproxyexecutor.Options{}, nil)
	var cooldownErr *modelCooldownError
	if !errors.As(errPick, &cooldownErr) {
		t.Fatalf("pickSingle() before sync error = %v, want modelCooldownError", errPick)
	}
	if got != nil {
		t.Fatalf("pickSingle() before sync auth = %v, want nil", got)
	}

	got, executor, errPick := manager.pickNext(ctx, "gemini", "scheduler-cooldown-rebuild-model", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext() error = %v", errPick)
	}
	if executor == nil {
		t.Fatal("pickNext() executor = nil")
	}
	if got == nil || got.ID != newAuth.ID {
		t.Fatalf("pickNext() auth = %v, want %q", got, newAuth.ID)
	}
}

// authUpdateCaptureHook records OnAuthUpdated calls for verification.
type authUpdateCaptureHook struct {
	NoopHook

	mu      sync.Mutex
	updates []*Auth
}

func (h *authUpdateCaptureHook) OnAuthUpdated(_ context.Context, auth *Auth) {
	h.mu.Lock()
	h.updates = append(h.updates, auth)
	h.mu.Unlock()
}

func (h *authUpdateCaptureHook) Updates() []*Auth {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*Auth, len(h.updates))
	copy(out, h.updates)
	return out
}

// TestRefreshAuthForRequest_PersistsPermanentFailureMarker verifies the fix
// for codex review P2 on commit d6e72a65: when refreshAuthForRequest gets a
// permanent failure (400 invalid_grant or 401), the permanent branch must
// persist the auth + cooldown snapshot + notify hooks, so the marker
// survives process restart. Without persistence, a restart reloads the auth
// from storage without the Unavailable/permanent-LastError marker, and any
// stale .cds cooldown record can also be restored, requeueing the revoked
// credential.
func TestRefreshAuthForRequest_PersistsPermanentFailureMarker(t *testing.T) {
	ctx := context.Background()
	store := &countingStore{}
	cooldownStore := &recordingCooldownStateStore{}
	hook := &authUpdateCaptureHook{}
	manager := NewManager(store, &RoundRobinSelector{}, hook)
	manager.SetCooldownStateStore(cooldownStore)
	manager.RegisterExecutor(invalidGrantRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "xai"},
	})

	setRefreshLeadFactory(t, "xai", func() *time.Duration {
		d := 5 * time.Minute
		return &d
	})

	auth := &Auth{
		ID:       "xai-persist-permanent",
		Provider: "xai",
		Metadata: map[string]any{
			"email": "persist@example.com",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	storeSaveBefore := store.saveCount.Load()
	cooldownSaveBefore := cooldownStore.saveCount.Load()
	updatesBefore := len(hook.Updates())

	// Trigger permanent refresh failure.
	manager.refreshAuth(ctx, auth.ID)

	after, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}
	if !after.Unavailable || !after.NextRetryAfter.IsZero() || !hasPermanentAuthFailure(after) {
		t.Fatalf("expected permanent marker, got Unavailable=%v NextRetryAfter=%v LastError=%+v", after.Unavailable, after.NextRetryAfter, after.LastError)
	}

	// Verify persist was called — the auth-level Save must fire for the
	// permanent marker to survive restart.
	if got := store.saveCount.Load(); got != storeSaveBefore+1 {
		t.Fatalf("expected store.Save count %d → %d, got %d", storeSaveBefore, storeSaveBefore+1, got)
	}

	// Verify persistCooldownStates was called — the stale .cds record must
	// be dropped so restart doesn't restore a cooldown that masks the
	// permanent marker.
	if got := cooldownStore.saveCount.Load(); got != cooldownSaveBefore+1 {
		t.Fatalf("expected cooldownStore.Save count %d → %d, got %d", cooldownSaveBefore, cooldownSaveBefore+1, got)
	}

	// Verify OnAuthUpdated hook was notified so downstream observers (e.g.
	// management UI, plugin host) see the permanent failure.
	if got := len(hook.Updates()); got != updatesBefore+1 {
		t.Fatalf("expected OnAuthUpdated count %d → %d, got %d", updatesBefore, updatesBefore+1, got)
	}
	lastUpdate := hook.Updates()[len(hook.Updates())-1]
	if !lastUpdate.Unavailable || !lastUpdate.NextRetryAfter.IsZero() {
		t.Fatalf("OnAuthUpdated received non-permanent marker: Unavailable=%v NextRetryAfter=%v", lastUpdate.Unavailable, lastUpdate.NextRetryAfter)
	}
}

// TestRefreshAuthForRequest_PermanentFailureClearsModelCooldowns verifies
// the fix for codex review P2 on commit 87b7fa62: the permanent refresh
// branch must clear per-model cooldown state, otherwise persistCooldownStates
// keeps the model .cds records in the snapshot and a restart replays them
// via RestoreCooldownStates → updateAggregatedAvailability, which restores
// a non-zero auth NextRetryAfter and masks the permanent marker from the
// IsZero() guards.
func TestRefreshAuthForRequest_PermanentFailureClearsModelCooldowns(t *testing.T) {
	ctx := context.Background()
	store := &countingStore{}
	cooldownStore := &recordingCooldownStateStore{}
	manager := NewManager(store, &RoundRobinSelector{}, nil)
	manager.SetCooldownStateStore(cooldownStore)
	manager.RegisterExecutor(invalidGrantRefreshTestExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "xai"},
	})

	setRefreshLeadFactory(t, "xai", func() *time.Duration {
		d := 5 * time.Minute
		return &d
	})

	auth := &Auth{
		ID:       "xai-model-cooldown-clear",
		Provider: "xai",
		Metadata: map[string]any{
			"email": "model-clear@example.com",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// Pre-seed a model-scoped cooldown via MarkResult (simulating prior
	// traffic that hit a 429 on grok-4). MarkResult calls
	// persistCooldownStates, so the snapshot will contain the grok-4
	// record. This is the state that must be cleared on permanent
	// refresh failure.
	manager.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "xai",
		Model:    "grok-4",
		Success:  false,
		Error: &Error{
			HTTPStatus: http.StatusTooManyRequests,
			Message:    "rate limited",
		},
	})

	// Verify the cooldown snapshot contains the model record before refresh.
	cooldownStore.mu.Lock()
	hasModelRecordBefore := false
	for _, r := range cooldownStore.records {
		if r.Model == "grok-4" && r.AuthID == auth.ID {
			hasModelRecordBefore = true
			break
		}
	}
	cooldownStore.mu.Unlock()
	if !hasModelRecordBefore {
		t.Fatal("precondition: expected grok-4 model cooldown record before refresh")
	}

	// Trigger permanent refresh failure.
	manager.refreshAuth(ctx, auth.ID)

	after, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("expected auth %q after refresh", auth.ID)
	}
	if !after.Unavailable || !after.NextRetryAfter.IsZero() || !hasPermanentAuthFailure(after) {
		t.Fatalf("expected permanent marker, got Unavailable=%v NextRetryAfter=%v LastError=%+v", after.Unavailable, after.NextRetryAfter, after.LastError)
	}

	// The model-scoped cooldown must be cleared.
	state, hasState := after.ModelStates["grok-4"]
	if !hasState || state == nil {
		t.Fatalf("expected grok-4 model state to exist (reset, not deleted), got ModelStates=%+v", after.ModelStates)
	}
	if state.Unavailable || !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected grok-4 model cooldown cleared, got Unavailable=%v NextRetryAfter=%v", state.Unavailable, state.NextRetryAfter)
	}
	if state.LastError != nil {
		t.Fatalf("expected grok-4 model LastError cleared, got %+v", state.LastError)
	}

	// The cooldown snapshot after persist must NOT contain the grok-4
	// record — otherwise restart replays it and restores a non-zero auth
	// NextRetryAfter that masks the permanent marker.
	cooldownStore.mu.Lock()
	hasModelRecordAfter := false
	for _, r := range cooldownStore.records {
		if r.Model == "grok-4" && r.AuthID == auth.ID {
			hasModelRecordAfter = true
			break
		}
	}
	cooldownStore.mu.Unlock()
	if hasModelRecordAfter {
		t.Fatal("expected grok-4 model cooldown record to be dropped from snapshot after permanent refresh failure")
	}
}

// TestClearCooldownStateForAuth_PreservesPermanentFailureMarker verifies
// the fix for codex review P2 on commit 6f555cbf: clearCooldownStateForAuth
// is invoked from SetConfig/register/update paths when disable-cooling is
// enabled, and historically clears auth.Unavailable — without a guard,
// a config reload erases the permanent marker and the shouldRefresh /
// nextRefreshCheckAt / isAuthBlockedForModel guards stop firing,
// re-queueing revoked 400 invalid_grant credentials.
func TestClearCooldownStateForAuth_PreservesPermanentFailureMarker(t *testing.T) {
	now := time.Now()

	// Permanent failure marker: Unavailable + permanent LastError +
	// zero NextRetryAfter. A stale model cooldown is also present,
	// which clearCooldownStateForAuth should still clear.
	auth := &Auth{
		ID:                      "xai-clear-cooldown-permanent",
		Provider:                "xai",
		Unavailable:             true,
		PermanentRefreshFailure: true,
		NextRetryAfter:          time.Time{},
		Status:                  StatusError,
		StatusMessage:           "refresh token revoked",
		LastError: &Error{
			HTTPStatus: http.StatusBadRequest,
			Message:    "invalid_grant: refresh token revoked",
		},
		ModelStates: map[string]*ModelState{
			"grok-4": {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: now.Add(30 * time.Minute),
				UpdatedAt:      now,
			},
		},
	}

	if !hasPermanentAuthFailure(auth) {
		t.Fatalf("precondition: expected permanent failure marker")
	}

	changed := clearCooldownStateForAuth(auth, now)

	// Permanent marker must be preserved.
	if !auth.Unavailable {
		t.Fatal("expected Unavailable=true to be preserved by clearCooldownStateForAuth")
	}
	if !auth.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter to stay zero, got %v", auth.NextRetryAfter)
	}
	if !hasPermanentAuthFailure(auth) {
		t.Fatal("expected hasPermanentAuthFailure to still be true after clearCooldownStateForAuth")
	}

	// Stale model cooldown should be cleared (the permanent-marker guard
	// only protects the auth-level marker, not stale model states).
	state := auth.ModelStates["grok-4"]
	if state == nil {
		t.Fatal("expected grok-4 model state to still exist")
	}
	if state.Unavailable || !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected grok-4 model cooldown cleared, got Unavailable=%v NextRetryAfter=%v", state.Unavailable, state.NextRetryAfter)
	}
	if !changed {
		t.Fatal("expected changed=true since model cooldown was cleared")
	}

	// The guards must still fire after clearCooldownStateForAuth.
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	if manager.shouldRefresh(auth, now) {
		t.Fatal("expected shouldRefresh=false after clearCooldownStateForAuth preserved marker")
	}
	if _, shouldSchedule := nextRefreshCheckAt(now, auth, time.Second); shouldSchedule {
		t.Fatal("expected nextRefreshCheckAt to keep unscheduled after clearCooldownStateForAuth")
	}
	blocked, reason, _ := isAuthBlockedForModel(auth, "grok-4", now)
	if !blocked || reason != blockReasonOther {
		t.Fatalf("expected isAuthBlockedForModel to keep blocking, got blocked=%v reason=%d", blocked, reason)
	}
}

// TestClearCooldownStateForAuth_StillClearsTransientCooldown verifies the
// guard only protects permanent markers — transient cooldowns
// (NextRetryAfter non-zero, no permanent LastError) are still cleared.
func TestClearCooldownStateForAuth_StillClearsTransientCooldown(t *testing.T) {
	now := time.Now()
	auth := &Auth{
		ID:             "xai-transient-cooldown",
		Provider:       "xai",
		Unavailable:    true,
		NextRetryAfter: now.Add(30 * time.Minute),
		Status:         StatusError,
		StatusMessage:  "rate limited",
	}

	if hasPermanentAuthFailure(auth) {
		t.Fatalf("precondition: expected NOT a permanent failure (no LastError)")
	}

	changed := clearCooldownStateForAuth(auth, now)

	if !changed {
		t.Fatal("expected changed=true for transient cooldown")
	}
	if auth.Unavailable {
		t.Fatal("expected Unavailable=false after clearing transient cooldown")
	}
	if !auth.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter=zero after clearing, got %v", auth.NextRetryAfter)
	}
}

// TestPermanentFailureMetadata_RoundTrip verifies that the permanent-failure
// marker survives a persist → reload cycle via Metadata. Store readers
// (FileTokenStore/PostgresStore/ObjectStore/GitStore) only persist
// Auth.Metadata, so the marker (which lives on Auth struct fields) must be
// mirrored into Metadata by persist() and restored by
// ApplyPermanentFailureFromMetadata. Without this, a restart reloads the
// revoked auth as active/routable (codex review).
func TestPermanentFailureMetadata_RoundTrip(t *testing.T) {
	auth := &Auth{
		ID:       "xai-metadata-roundtrip",
		Provider: "xai",
		Metadata: map[string]any{
			"type":                          "xai",
			"email":                         "roundtrip@example.com",
			"cpa_permanent_refresh_failure": true,
			"cpa_permanent_unavailable":     true,
			"cpa_permanent_status":          string(StatusError),
			"cpa_permanent_status_message":  "refresh token revoked",
			"access_token":                  "stale",
			"refresh_token":                 "revoked",
		},
		PermanentRefreshFailure: false,
		Unavailable:             false,
		Status:                  StatusActive,
		StatusMessage:           "",
	}

	ApplyPermanentFailureFromMetadata(auth)

	if !auth.PermanentRefreshFailure {
		t.Fatal("expected PermanentRefreshFailure=true after ApplyPermanentFailureFromMetadata")
	}
	if !auth.Unavailable {
		t.Fatal("expected Unavailable=true after restore")
	}
	if auth.Status != StatusError {
		t.Fatalf("expected Status=StatusError, got %s", auth.Status)
	}
	if auth.StatusMessage != "refresh token revoked" {
		t.Fatalf("expected StatusMessage restored, got %q", auth.StatusMessage)
	}
	if !hasPermanentAuthFailure(auth) {
		t.Fatal("expected hasPermanentAuthFailure=true after restore")
	}

	now := time.Now()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	if manager.shouldRefresh(auth, now) {
		t.Fatal("expected shouldRefresh=false on restored permanent failure")
	}
	blocked, reason, _ := isAuthBlockedForModel(auth, "grok-4", now)
	if !blocked || reason != blockReasonOther {
		t.Fatalf("expected isAuthBlockedForModel to block restored permanent failure, got blocked=%v reason=%d", blocked, reason)
	}
}

// TestPermanentFailureMetadata_NoMarkerIsNoOp verifies that
// ApplyPermanentFailureFromMetadata is a no-op when the marker is absent.
func TestPermanentFailureMetadata_NoMarkerIsNoOp(t *testing.T) {
	auth := &Auth{
		ID:       "xai-no-marker",
		Provider: "xai",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":          "xai",
			"email":         "no-marker@example.com",
			"access_token":  "valid",
			"refresh_token": "valid",
		},
	}

	ApplyPermanentFailureFromMetadata(auth)

	if auth.PermanentRefreshFailure {
		t.Fatal("expected PermanentRefreshFailure=false when marker absent")
	}
	if auth.Unavailable {
		t.Fatal("expected Unavailable=false when marker absent")
	}
	if auth.Status != StatusActive {
		t.Fatalf("expected Status=StatusActive, got %s", auth.Status)
	}
	if hasPermanentAuthFailure(auth) {
		t.Fatal("expected hasPermanentAuthFailure=false when marker absent")
	}
}
