package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type quotaProbeTestExecutor struct {
	status int
	body   string
	err    error
	calls  atomic.Int32
}

func (e *quotaProbeTestExecutor) Identifier() string { return "codex" }

func (e *quotaProbeTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *quotaProbeTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *quotaProbeTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *quotaProbeTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *quotaProbeTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	e.calls.Add(1)
	if e.err != nil {
		return nil, e.err
	}
	return &http.Response{
		StatusCode: e.status,
		Body:       io.NopCloser(strings.NewReader(e.body)),
		Header:     make(http.Header),
	}, nil
}

func registerUsageLimitAuth(t *testing.T, manager *Manager, id, accountID, planType string, retryAfter time.Duration) {
	t.Helper()
	_, errRegister := manager.Register(context.Background(), &Auth{
		ID:       id,
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"account_id": accountID,
			"plan_type":  planType,
		},
	})
	if errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	manager.MarkResult(context.Background(), Result{
		AuthID:     id,
		Provider:   "codex",
		RetryAfter: &retryAfter,
		Error: &Error{
			HTTPStatus: http.StatusTooManyRequests,
			Code:       "usage_limit_reached",
			Message:    `{"error":{"type":"usage_limit_reached"}}`,
		},
	})
	manager.scheduleCodexQuotaProbeNow(id)
}

func TestNextCodexQuotaProbeAt_FarNearAndResetBoundary(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	if got := nextCodexQuotaProbeAt(now, now.Add(7*24*time.Hour)); !got.Equal(now.Add(30 * time.Minute)) {
		t.Fatalf("far probe = %v, want %v", got, now.Add(30*time.Minute))
	}
	if got := nextCodexQuotaProbeAt(now, now.Add(90*time.Minute)); !got.Equal(now.Add(5 * time.Minute)) {
		t.Fatalf("near probe = %v, want %v", got, now.Add(5*time.Minute))
	}
	if got := nextCodexQuotaProbeAt(now, now.Add(2*time.Minute)); !got.Equal(now.Add(2 * time.Minute)) {
		t.Fatalf("boundary probe = %v, want provider reset", got)
	}
}

func TestManager_CollectCodexQuotaProbeBuckets_DeduplicatesSharedQuota(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	enableAdaptiveCooldown(manager, 0)
	retryAfter := 7 * 24 * time.Hour
	registerUsageLimitAuth(t, manager, "auth-a", "acct-shared", "plus", retryAfter)
	registerUsageLimitAuth(t, manager, "auth-b", "acct-shared", "plus", retryAfter)

	buckets, _ := manager.collectCodexQuotaProbeBuckets(time.Now())
	if len(buckets) != 1 {
		t.Fatalf("probe buckets = %d, want 1", len(buckets))
	}
	if len(buckets[0].authIDs) != 2 {
		t.Fatalf("bucket auth ids = %v, want two auths", buckets[0].authIDs)
	}
}

func TestManager_CodexQuotaProbe_RecoveryResetsWholeSharedBucket(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	enableAdaptiveCooldown(manager, 0)
	executor := &quotaProbeTestExecutor{
		status: http.StatusOK,
		body:   `{"plan_type":"plus","rate_limit":{"limit_reached":false,"primary_window":{"used_percent":42,"reset_at":1700003600}}}`,
	}
	manager.RegisterExecutor(executor)
	retryAfter := 7 * 24 * time.Hour
	registerUsageLimitAuth(t, manager, "auth-a", "acct-shared", "plus", retryAfter)
	registerUsageLimitAuth(t, manager, "auth-b", "acct-shared", "plus", retryAfter)

	buckets, _ := manager.collectCodexQuotaProbeBuckets(time.Now())
	outcome := manager.probeCodexQuotaBucket(context.Background(), buckets[0])
	if outcome.err != nil || !outcome.recovered {
		t.Fatalf("probe outcome = %+v, want recovered", outcome)
	}
	manager.applyCodexQuotaProbeOutcome(context.Background(), buckets[0].key, outcome, time.Now())

	if got := executor.calls.Load(); got != 1 {
		t.Fatalf("WHAM calls = %d, want 1", got)
	}
	for _, id := range []string{"auth-a", "auth-b"} {
		auth, _ := manager.GetByID(id)
		if auth.Unavailable || auth.Quota.Exceeded || !auth.NextRetryAfter.IsZero() {
			t.Fatalf("%s remained quota blocked: %+v", id, auth.Quota)
		}
	}
}

func TestManager_StartCodexQuotaProbe_ProcessesImmediateRecovery(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	enableAdaptiveCooldown(manager, 0)
	executor := &quotaProbeTestExecutor{
		status: http.StatusOK,
		body:   `{"plan_type":"plus","rate_limit":{"limit_reached":false,"primary_window":{"used_percent":1}}}`,
	}
	manager.RegisterExecutor(executor)
	registerUsageLimitAuth(t, manager, "auth-loop", "acct-loop", "plus", 7*24*time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.StartCodexQuotaProbe(ctx)
	defer manager.StopCodexQuotaProbe()

	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			t.Fatalf("quota probe loop did not recover auth; calls=%d", executor.calls.Load())
		case <-ticker.C:
			auth, _ := manager.GetByID("auth-loop")
			if executor.calls.Load() > 0 && auth != nil && !auth.Unavailable && !auth.Quota.Exceeded {
				return
			}
		}
	}
}

func TestManager_CodexQuotaProbe_FailureNeverUnlocks(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	enableAdaptiveCooldown(manager, 0)
	executor := &quotaProbeTestExecutor{status: http.StatusBadGateway, body: `{"error":"temporary"}`}
	manager.RegisterExecutor(executor)
	retryAfter := 90 * time.Minute
	registerUsageLimitAuth(t, manager, "auth-failure", "acct-failure", "plus", retryAfter)

	buckets, _ := manager.collectCodexQuotaProbeBuckets(time.Now())
	outcome := manager.probeCodexQuotaBucket(context.Background(), buckets[0])
	if outcome.err == nil || outcome.recovered {
		t.Fatalf("probe outcome = %+v, want failed and blocked", outcome)
	}
	now := time.Now()
	manager.applyCodexQuotaProbeOutcome(context.Background(), buckets[0].key, outcome, now)
	auth, _ := manager.GetByID("auth-failure")
	if !auth.Unavailable || !auth.Quota.Exceeded {
		t.Fatalf("failed probe unlocked auth: %+v", auth.Quota)
	}
	if !auth.Quota.NextProbeAt.After(now) {
		t.Fatalf("next probe = %v, want after %v", auth.Quota.NextProbeAt, now)
	}
}

func TestManager_CodexQuotaProbe_LimitReachedUpdatesProviderReset(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	enableAdaptiveCooldown(manager, 0)
	resetAt := time.Now().Add(90 * time.Minute).Truncate(time.Second)
	executor := &quotaProbeTestExecutor{
		status: http.StatusOK,
		body:   fmt.Sprintf(`{"plan_type":"plus","rate_limit":{"limit_reached":true,"primary_window":{"used_percent":100,"reset_at":%d}}}`, resetAt.Unix()),
	}
	manager.RegisterExecutor(executor)
	registerUsageLimitAuth(t, manager, "auth-limited", "acct-limited", "plus", 7*24*time.Hour)

	buckets, _ := manager.collectCodexQuotaProbeBuckets(time.Now())
	outcome := manager.probeCodexQuotaBucket(context.Background(), buckets[0])
	if outcome.err != nil || outcome.recovered {
		t.Fatalf("probe outcome = %+v, want still limited", outcome)
	}
	manager.applyCodexQuotaProbeOutcome(context.Background(), buckets[0].key, outcome, time.Now())
	auth, _ := manager.GetByID("auth-limited")
	if !auth.Quota.NextRecoverAt.Equal(resetAt) {
		t.Fatalf("provider reset = %v, want %v", auth.Quota.NextRecoverAt, resetAt)
	}
}

func TestManager_CodexQuotaProbe_MissingFieldsNeverUnlocks(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	enableAdaptiveCooldown(manager, 0)
	executor := &quotaProbeTestExecutor{status: http.StatusOK, body: `{"rate_limit":{"limit_reached":false,"primary_window":{}}}`}
	manager.RegisterExecutor(executor)
	registerUsageLimitAuth(t, manager, "auth-missing", "acct-missing", "plus", 7*24*time.Hour)

	buckets, _ := manager.collectCodexQuotaProbeBuckets(time.Now())
	outcome := manager.probeCodexQuotaBucket(context.Background(), buckets[0])
	if outcome.err == nil || outcome.recovered {
		t.Fatalf("probe outcome = %+v, want missing-field failure", outcome)
	}
}

func TestManager_RefreshSchedulesImmediateQuotaProbe(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	enableAdaptiveCooldown(manager, 0)
	executor := &quotaProbeTestExecutor{status: http.StatusOK, body: `{}`}
	manager.RegisterExecutor(executor)
	retryAfter := 7 * 24 * time.Hour
	registerUsageLimitAuth(t, manager, "auth-refresh", "acct-refresh", "plus", retryAfter)

	authBefore, _ := manager.GetByID("auth-refresh")
	authBefore.Quota.NextProbeAt = time.Now().Add(time.Hour)
	if _, errUpdate := manager.Update(context.Background(), authBefore); errUpdate != nil {
		t.Fatalf("set future probe: %v", errUpdate)
	}
	before := time.Now()
	refreshed, errRefresh := manager.refreshAuthForRequest(context.Background(), "auth-refresh", "")
	if errRefresh != nil {
		t.Fatalf("refresh auth: %v", errRefresh)
	}
	after := time.Now()
	updated, _ := manager.GetByID("auth-refresh")
	if refreshed == nil || !refreshed.Unavailable || !refreshed.Quota.Exceeded {
		t.Fatalf("refresh returned prematurely unlocked auth: %+v", refreshed)
	}
	if !updated.Unavailable || !updated.Quota.Exceeded {
		t.Fatalf("refresh unlocked usage-limited auth before probe: %+v", updated.Quota)
	}
	if updated.Quota.NextProbeAt.Before(before) || updated.Quota.NextProbeAt.After(after.Add(cooldownTestTolerance)) {
		t.Fatalf("next probe after refresh = %v, want immediate within [%v, %v]", updated.Quota.NextProbeAt, before, after)
	}
}

func TestManager_DisablingAdaptiveCooldownDowngradesUsageState(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	enableAdaptiveCooldown(manager, 0)
	retryAfter := time.Hour
	registerUsageLimitAuth(t, manager, "auth-disable", "acct-disable", "plus", retryAfter)
	before, _ := manager.GetByID("auth-disable")
	providerReset := before.Quota.NextRecoverAt

	manager.SetConfig(&internalconfig.Config{})
	updated, _ := manager.GetByID("auth-disable")
	if updated.Quota.Reason != "quota" || !updated.Quota.NextProbeAt.IsZero() {
		t.Fatalf("disabled adaptive state = %+v, want legacy quota state", updated.Quota)
	}
	if !updated.Unavailable || !updated.NextRetryAfter.Equal(providerReset) {
		t.Fatalf("disabled adaptive state unlocked early: unavailable=%t next=%v reset=%v", updated.Unavailable, updated.NextRetryAfter, providerReset)
	}
}

func TestIsAuthBlockedForModel_UsageLimitRemainsBlockedAfterResetUntilProbe(t *testing.T) {
	now := time.Now()
	auth := &Auth{
		ID:          "auth-expired-reset",
		Provider:    "codex",
		Unavailable: true,
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "usage_limit_reached",
			NextRecoverAt: now.Add(-time.Minute),
			NextProbeAt:   now.Add(time.Minute),
		},
		NextRetryAfter: now.Add(-time.Minute),
	}
	blocked, reason, next := isAuthBlockedForModel(auth, "", now)
	if !blocked || reason != blockReasonCooldown || !next.Equal(auth.Quota.NextProbeAt) {
		t.Fatalf("blocked=%t reason=%v next=%v, want usage-limit block until %v", blocked, reason, next, auth.Quota.NextProbeAt)
	}
}
