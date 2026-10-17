package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type testRefreshEvaluator struct{}

func (testRefreshEvaluator) ShouldRefresh(time.Time, *Auth) bool { return false }

func setRefreshLeadFactory(t *testing.T, provider string, factory func() *time.Duration) {
	t.Helper()
	key := strings.ToLower(strings.TrimSpace(provider))
	refreshLeadMu.Lock()
	prev, hadPrev := refreshLeadFactories[key]
	if factory == nil {
		delete(refreshLeadFactories, key)
	} else {
		refreshLeadFactories[key] = factory
	}
	refreshLeadMu.Unlock()
	t.Cleanup(func() {
		refreshLeadMu.Lock()
		if hadPrev {
			refreshLeadFactories[key] = prev
		} else {
			delete(refreshLeadFactories, key)
		}
		refreshLeadMu.Unlock()
	})
}

func TestNextRefreshCheckAt_DisabledWithInvalidGrantUnschedule(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	expiry := now.Add(time.Hour)
	lead := 10 * time.Minute
	setRefreshLeadFactory(t, "disabled-schedule", func() *time.Duration {
		d := lead
		return &d
	})

	// Case 1: Normal disabled credential WITHOUT invalid_grant is scheduled for token refresh (expected behavior)
	normalDisabledAuth := &Auth{
		ID:       "normal-disabled",
		Provider: "disabled-schedule",
		Disabled: true,
		Status:   StatusDisabled,
		Metadata: map[string]any{
			"email":      "x@example.com",
			"expires_at": expiry.Format(time.RFC3339),
		},
	}
	got, ok := nextRefreshCheckAt(now, normalDisabledAuth, 15*time.Minute)
	if !ok {
		t.Fatalf("nextRefreshCheckAt() ok = false, want true for normal disabled auth")
	}
	want := expiry.Add(-lead)
	if !got.Equal(want) {
		t.Fatalf("nextRefreshCheckAt() = %s, want %s", got, want)
	}

	// Case 2: Disabled credential WITH invalid_grant is permanently unscheduled (never refreshed)
	invalidGrantDisabledAuth := &Auth{
		ID:       "invalid-grant-disabled",
		Provider: "disabled-schedule",
		Disabled: true,
		Status:   StatusDisabled,
		LastError: &Error{
			HTTPStatus: 400,
			Message:    `{"error": "invalid_grant", "error_description": "Bad Request"}`,
		},
		Metadata: map[string]any{
			"email":      "x@example.com",
			"expires_at": expiry.Format(time.RFC3339),
		},
	}
	if _, ok := nextRefreshCheckAt(now, invalidGrantDisabledAuth, 15*time.Minute); ok {
		t.Fatalf("nextRefreshCheckAt() ok = true, want false for disabled auth with invalid_grant")
	}
}

func TestNextRefreshCheckAt_APIKeyUnschedule(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	auth := &Auth{ID: "a1", Provider: "test", Attributes: map[string]string{"api_key": "k"}}
	if _, ok := nextRefreshCheckAt(now, auth, 15*time.Minute); ok {
		t.Fatalf("nextRefreshCheckAt() ok = true, want false")
	}
}

func TestNextRefreshCheckAt_NextRefreshAfterGate(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	nextAfter := now.Add(30 * time.Minute)
	auth := &Auth{
		ID:               "a1",
		Provider:         "test",
		NextRefreshAfter: nextAfter,
		Metadata:         map[string]any{"email": "x@example.com"},
	}
	got, ok := nextRefreshCheckAt(now, auth, 15*time.Minute)
	if !ok {
		t.Fatalf("nextRefreshCheckAt() ok = false, want true")
	}
	if !got.Equal(nextAfter) {
		t.Fatalf("nextRefreshCheckAt() = %s, want %s", got, nextAfter)
	}
}

func TestNextRefreshCheckAt_PreferredInterval_PicksEarliestCandidate(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	expiry := now.Add(20 * time.Minute)
	auth := &Auth{
		ID:              "a1",
		Provider:        "test",
		LastRefreshedAt: now,
		Metadata: map[string]any{
			"email":                    "x@example.com",
			"expires_at":               expiry.Format(time.RFC3339),
			"refresh_interval_seconds": 900, // 15m
		},
	}
	got, ok := nextRefreshCheckAt(now, auth, 15*time.Minute)
	if !ok {
		t.Fatalf("nextRefreshCheckAt() ok = false, want true")
	}
	want := expiry.Add(-15 * time.Minute)
	if !got.Equal(want) {
		t.Fatalf("nextRefreshCheckAt() = %s, want %s", got, want)
	}
}

func TestNextRefreshCheckAt_ProviderLead_Expiry(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	expiry := now.Add(time.Hour)
	lead := 10 * time.Minute
	setRefreshLeadFactory(t, "provider-lead-expiry", func() *time.Duration {
		d := lead
		return &d
	})

	auth := &Auth{
		ID:       "a1",
		Provider: "provider-lead-expiry",
		Metadata: map[string]any{
			"email":      "x@example.com",
			"expires_at": expiry.Format(time.RFC3339),
		},
	}

	got, ok := nextRefreshCheckAt(now, auth, 15*time.Minute)
	if !ok {
		t.Fatalf("nextRefreshCheckAt() ok = false, want true")
	}
	want := expiry.Add(-lead)
	if !got.Equal(want) {
		t.Fatalf("nextRefreshCheckAt() = %s, want %s", got, want)
	}
}

func TestNextRefreshCheckAt_RelativeExpiry(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	issuedAt := now.Add(-15 * time.Minute)
	lead := 30 * time.Minute
	setRefreshLeadFactory(t, "relative-expiry", func() *time.Duration {
		d := lead
		return &d
	})

	auth := &Auth{
		ID:       "relative-expiry-auth",
		Provider: "relative-expiry",
		Metadata: map[string]any{
			"access_token": "test-access",
			"expires_in":   3600,
			"timestamp":    int(issuedAt.UnixMilli()),
		},
	}

	got, ok := nextRefreshCheckAt(now, auth, 15*time.Minute)
	want := issuedAt.Add(time.Hour - lead)
	if !ok || !got.Equal(want) {
		t.Fatalf("nextRefreshCheckAt() = (%s, %t), want (%s, true)", got, ok, want)
	}
}

func TestNextRefreshCheckAt_RefreshEvaluatorFallback(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	interval := 15 * time.Minute
	auth := &Auth{
		ID:       "a1",
		Provider: "test",
		Metadata: map[string]any{"email": "x@example.com"},
		Runtime:  testRefreshEvaluator{},
	}
	got, ok := nextRefreshCheckAt(now, auth, interval)
	if !ok {
		t.Fatalf("nextRefreshCheckAt() ok = false, want true")
	}
	want := now.Add(interval)
	if !got.Equal(want) {
		t.Fatalf("nextRefreshCheckAt() = %s, want %s", got, want)
	}
}

func TestAuthAutoRefreshLoop_TimerWaitClampedForLongNextExpiry(t *testing.T) {
	loop := newAuthAutoRefreshLoop(nil, 5*time.Second, 1)
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	loop.upsert("a1", now.Add(time.Hour))

	got, ok := loop.nextWait(now)
	if !ok {
		t.Fatalf("nextWait() ok = false, want true")
	}
	if got > maxRefreshTimerWait {
		t.Fatalf("nextWait() = %v, want <= %v", got, maxRefreshTimerWait)
	}
	if got != maxRefreshTimerWait {
		t.Fatalf("nextWait() = %v, want exactly %v", got, maxRefreshTimerWait)
	}

	// Short wait should not be clamped
	shortNext := now.Add(10 * time.Second)
	loop.upsert("a2", shortNext)
	gotShort, okShort := loop.nextWait(now)
	if !okShort || gotShort != 10*time.Second {
		t.Fatalf("nextWait() short = (%v, %t), want (10s, true)", gotShort, okShort)
	}
}

func TestAuthAutoRefreshLoop_PopDue_AfterSystemSuspendResume(t *testing.T) {
	loop := newAuthAutoRefreshLoop(nil, 5*time.Second, 1)
	beforeSleep := time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC)

	// Credential scheduled to refresh 50 minutes later
	scheduledRefresh := beforeSleep.Add(50 * time.Minute)
	loop.upsert("gemini-oauth", scheduledRefresh)

	// Before sleep, wait is clamped to maxRefreshTimerWait
	wait, ok := loop.nextWait(beforeSleep)
	if !ok || wait != maxRefreshTimerWait {
		t.Fatalf("nextWait() before sleep = (%v, %t), want (%v, true)", wait, ok, maxRefreshTimerWait)
	}

	// Not due yet before sleep
	dueBefore := loop.popDue(beforeSleep)
	if len(dueBefore) != 0 {
		t.Fatalf("popDue() before sleep = %v, want empty", dueBefore)
	}

	// System resumes 2 hours later (monotonic clock froze, but wall clock jumped)
	afterResume := beforeSleep.Add(2 * time.Hour)

	// Upon wake, the clamped timer fires within maxRefreshTimerWait and checks with current wall clock
	waitAfter, okAfter := loop.nextWait(afterResume)
	if !okAfter || waitAfter != 0 {
		t.Fatalf("nextWait() after resume = (%v, %t), want (0, true)", waitAfter, okAfter)
	}

	dueAfter := loop.popDue(afterResume)
	if len(dueAfter) != 1 || dueAfter[0] != "gemini-oauth" {
		t.Fatalf("popDue() after resume = %v, want [gemini-oauth]", dueAfter)
	}
}

func TestCodexDualRefreshScheduling(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	setRefreshLeadFactory(t, "codex", func() *time.Duration {
		d := 24 * time.Hour
		return &d
	})
	mgr := NewManager(nil, nil, nil)

	// Case 1: Outer expires in 5 days (due in 4 days), SIWC expires in 15 minutes (due in 10 minutes with 5m lead)
	// Minimum trigger should be SIWC due time (now + 10m), and shouldRefresh at now is false; at now + 10m is true.
	outerExp := now.Add(5 * 24 * time.Hour)
	siwcExp := now.Add(15 * time.Minute)
	auth := &Auth{
		ID:       "codex-dual.json",
		Provider: "codex",
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  "outer-access",
			"refresh_token": "outer-refresh",
			"expired":       outerExp.Format(time.RFC3339),
			"use_siwc":      true,
			"siwc": map[string]any{
				"access_token":  "siwc-access",
				"refresh_token": "siwc-refresh",
				"client_id":     "oaiapp_123",
				"expired":       siwcExp.Format(time.RFC3339),
			},
		},
	}

	nextAt, ok := nextRefreshCheckAt(now, auth, 5*time.Second)
	if !ok {
		t.Fatal("expected nextRefreshCheckAt ok=true")
	}
	wantSIWCDue := siwcExp.Add(-5 * time.Minute)
	if !nextAt.Equal(wantSIWCDue) {
		t.Fatalf("nextRefreshCheckAt = %v, want %v (min of outer and siwc)", nextAt, wantSIWCDue)
	}
	if mgr.shouldRefresh(auth, now) {
		t.Fatal("shouldRefresh at now = true, want false")
	}
	if !mgr.shouldRefresh(auth, now.Add(10*time.Minute)) {
		t.Fatal("shouldRefresh at now+10m = false, want true (SIWC within 5m lead)")
	}
	_, hasOuter, needOuter := CodexOuterNeedsRefresh(auth, now.Add(10*time.Minute))
	_, hasSIWC, needSIWC := CodexSIWCNeedsRefresh(auth, now.Add(10*time.Minute))
	if !hasOuter || needOuter {
		t.Fatalf("outer at now+10m: has=%v need=%v, want has=true need=false", hasOuter, needOuter)
	}
	if !hasSIWC || !needSIWC {
		t.Fatalf("siwc at now+10m: has=%v need=%v, want has=true need=true", hasSIWC, needSIWC)
	}

	// Case 2: Outer expires in 20 hours (due now since lead is 24h), SIWC expires in 50 minutes (due in 45m)
	auth.Metadata["expired"] = now.Add(20 * time.Hour).Format(time.RFC3339)
	auth.Metadata["siwc"].(map[string]any)["expired"] = now.Add(50 * time.Minute).Format(time.RFC3339)
	nextAt2, ok2 := nextRefreshCheckAt(now, auth, 5*time.Second)
	if !ok2 || !nextAt2.Equal(now) {
		t.Fatalf("nextRefreshCheckAt when outer due = (%v, %v), want (%v, true)", nextAt2, ok2, now)
	}
	if !mgr.shouldRefresh(auth, now) {
		t.Fatal("shouldRefresh when outer due = false, want true")
	}
	_, _, needOuter2 := CodexOuterNeedsRefresh(auth, now)
	_, _, needSIWC2 := CodexSIWCNeedsRefresh(auth, now)
	if !needOuter2 || needSIWC2 {
		t.Fatalf("at now: needOuter=%v needSIWC=%v, want true/false", needOuter2, needSIWC2)
	}
}

func TestCodexUseSIWCActiveAccessTokenAndValidity(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	// Outer expired 1 hour ago, SIWC valid for 30 minutes, use_siwc = true
	auth := &Auth{
		ID:       "codex-active-siwc.json",
		Provider: "codex",
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "outer-expired-token",
			"expired":      now.Add(-1 * time.Hour).Format(time.RFC3339),
			"use_siwc":     true,
			"siwc": map[string]any{
				"access_token":  "siwc-valid-token",
				"refresh_token": "siwc-ref",
				"client_id":     "oaiapp_1",
				"expired":       now.Add(30 * time.Minute).Format(time.RFC3339),
			},
		},
	}
	if gotTok := authAccessToken(auth); gotTok != "siwc-valid-token" {
		t.Fatalf("authAccessToken(use_siwc=true) = %q, want siwc-valid-token", gotTok)
	}
	if !auth.HasValidAccessToken(now) {
		t.Fatal("HasValidAccessToken(use_siwc=true with valid SIWC) = false, want true")
	}

	// Now SIWC is expired while outer is valid for 5 days
	auth.Metadata["expired"] = now.Add(5 * 24 * time.Hour).Format(time.RFC3339)
	auth.Metadata["siwc"].(map[string]any)["expired"] = now.Add(-1 * time.Minute).Format(time.RFC3339)
	if auth.HasValidAccessToken(now) {
		t.Fatal("HasValidAccessToken(use_siwc=true with expired SIWC) = true, want false")
	}
}

func TestCodexSIWCRefreshDoesNotPostponeOuterIntervalRefresh(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	auth := &Auth{
		ID:       "codex-interval.json",
		Provider: "codex",
		// Shared LastRefreshedAt was just updated by an SIWC refresh 5 minutes ago
		LastRefreshedAt: now.Add(-5 * time.Minute),
		Metadata: map[string]any{
			"type":                     "codex",
			"access_token":             "outer-tok",
			"refresh_token":            "outer-ref",
			"refresh_interval_seconds": 3600,
			// Outer was refreshed 55 minutes ago, so it should be due in 5 minutes (now + 5m), NOT now + 55m
			"last_refresh": now.Add(-55 * time.Minute).Format(time.RFC3339),
			"expired":      now.Add(7 * 24 * time.Hour).Format(time.RFC3339),
			"use_siwc":     true,
			"siwc": map[string]any{
				"access_token":  "siwc-tok",
				"refresh_token": "siwc-ref",
				"client_id":     "oaiapp_1",
				"last_refresh":  now.Add(-5 * time.Minute).Format(time.RFC3339),
				"expired":       now.Add(52 * time.Minute).Format(time.RFC3339),
			},
		},
	}
	dueOuter, hasOuter, needOuter := CodexOuterNeedsRefresh(auth, now)
	if !hasOuter || needOuter {
		t.Fatalf("hasOuter=%v needOuter=%v, want true/false", hasOuter, needOuter)
	}
	wantDue := now.Add(5 * time.Minute)
	if !dueOuter.Equal(wantDue) {
		t.Fatalf("outer dueAt = %v, want %v (must not be postponed by shared LastRefreshedAt)", dueOuter, wantDue)
	}
	_, _, needOuterAtDue := CodexOuterNeedsRefresh(auth, now.Add(6*time.Minute))
	if !needOuterAtDue {
		t.Fatal("outer should need refresh after its own interval elapses")
	}
}

type blockingCodexRefreshExecutor struct {
	started chan struct{}
	release chan struct{}
}

func (e *blockingCodexRefreshExecutor) Identifier() string { return "codex" }
func (e *blockingCodexRefreshExecutor) PrepareRequest(_ *http.Request, _ *Auth) error {
	return nil
}
func (e *blockingCodexRefreshExecutor) HttpRequest(_ context.Context, _ *Auth, _ *http.Request) (*http.Response, error) {
	return nil, nil
}
func (e *blockingCodexRefreshExecutor) Execute(_ context.Context, _ *Auth, _ executor.Request, _ executor.Options) (executor.Response, error) {
	return executor.Response{}, nil
}
func (e *blockingCodexRefreshExecutor) ExecuteStream(_ context.Context, _ *Auth, _ executor.Request, _ executor.Options) (*executor.StreamResult, error) {
	return nil, nil
}
func (e *blockingCodexRefreshExecutor) CountTokens(_ context.Context, _ *Auth, _ executor.Request, _ executor.Options) (executor.Response, error) {
	return executor.Response{}, nil
}
func (e *blockingCodexRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	close(e.started)
	<-e.release
	updated := auth.Clone()
	updated.Metadata["access_token"] = "outer-rotated-access"
	updated.Metadata["refresh_token"] = "outer-rotated-refresh"
	updated.Metadata["last_refresh"] = "2026-10-12T12:01:00Z"
	updated.CodexRefreshPatch = codex.NewCredentialRefreshPatch(auth.Metadata, updated.Metadata, true, false)
	return updated, nil
}

type simpleCodexDiskStore struct {
	path string
}

func (s *simpleCodexDiskStore) List(_ context.Context) ([]*Auth, error)  { return nil, nil }
func (s *simpleCodexDiskStore) Delete(_ context.Context, _ string) error { return nil }
func (s *simpleCodexDiskStore) Save(ctx context.Context, auth *Auth) (string, error) {
	if _, err := PersistCodexAuthFile(ctx, s.path, auth); err != nil {
		return "", err
	}
	return s.path, nil
}

func TestManagerRefreshOuterPreservesConcurrentSIWCBind(t *testing.T) {
	tmpDir := t.TempDir()
	authPath := filepath.Join(tmpDir, "codex-concurrent.json")
	initialMeta := map[string]any{
		"type":          "codex",
		"access_token":  "outer-initial-access",
		"refresh_token": "outer-initial-refresh",
		"last_refresh":  "2026-10-12T10:00:00Z",
	}
	if err := codex.WriteJSONFileAtomic(authPath, initialMeta); err != nil {
		t.Fatalf("WriteJSONFileAtomic: %v", err)
	}

	store := &simpleCodexDiskStore{path: authPath}
	m := NewManager(store, nil, nil)
	exec := &blockingCodexRefreshExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	m.RegisterExecutor(exec)

	initialAuth := &Auth{
		ID:       "codex-concurrent.json",
		FileName: "codex-concurrent.json",
		Provider: "codex",
		Metadata: initialMeta,
	}
	if _, err := m.Register(context.Background(), initialAuth); err != nil {
		t.Fatalf("Register: %v", err)
	}

	doneRefresh := make(chan struct{})
	go func() {
		defer close(doneRefresh)
		m.refreshAuth(context.Background(), "codex-concurrent.json")
	}()

	<-exec.started

	// While outer refresh is in flight, simulate -siwc-bind updating disk and watcher updating Manager
	boundMeta := map[string]any{
		"type":          "codex",
		"access_token":  "outer-initial-access",
		"refresh_token": "outer-initial-refresh",
		"last_refresh":  "2026-10-12T10:00:00Z",
		"use_siwc":      true,
		"siwc": map[string]any{
			"access_token":  "siwc-bound-access",
			"refresh_token": "siwc-bound-refresh",
			"client_id":     "oaiapp_bound",
			"last_refresh":  "2026-10-12T12:00:30Z",
		},
	}
	if err := codex.WriteJSONFileAtomic(authPath, boundMeta); err != nil {
		t.Fatalf("WriteJSONFileAtomic bound: %v", err)
	}
	boundAuth := initialAuth.Clone()
	boundAuth.Metadata = boundMeta
	if _, err := m.Update(context.Background(), boundAuth); err != nil {
		t.Fatalf("Manager.Update bound: %v", err)
	}

	close(exec.release)
	<-doneRefresh

	// Verify both Manager and disk have the rotated outer token AND the concurrently bound SIWC section
	finalAuth, ok := m.GetByID("codex-concurrent.json")
	if !ok || finalAuth == nil {
		t.Fatal("finalAuth not found in Manager")
	}
	if finalAuth.Metadata["refresh_token"] != "outer-rotated-refresh" || finalAuth.Metadata["access_token"] != "outer-rotated-access" {
		t.Fatalf("Manager lost rotated outer token after concurrent SIWC bind: %#v", finalAuth.Metadata)
	}
	if finalAuth.Metadata["use_siwc"] != true {
		t.Fatalf("Manager lost use_siwc=true: %#v", finalAuth.Metadata)
	}
	siwcMem, _ := finalAuth.Metadata["siwc"].(map[string]any)
	if siwcMem["access_token"] != "siwc-bound-access" || siwcMem["refresh_token"] != "siwc-bound-refresh" {
		t.Fatalf("Manager lost concurrent SIWC bind: %#v", siwcMem)
	}

	rawDisk, errRead := os.ReadFile(authPath)
	if errRead != nil {
		t.Fatalf("ReadFile: %v", errRead)
	}
	var diskMap map[string]any
	if errUnmarshal := json.Unmarshal(rawDisk, &diskMap); errUnmarshal != nil {
		t.Fatalf("Unmarshal: %v", errUnmarshal)
	}
	if diskMap["refresh_token"] != "outer-rotated-refresh" || diskMap["use_siwc"] != true {
		t.Fatalf("disk lost rotated outer token or use_siwc: %#v", diskMap)
	}
	siwcDisk, _ := diskMap["siwc"].(map[string]any)
	if siwcDisk["access_token"] != "siwc-bound-access" {
		t.Fatalf("disk lost concurrent SIWC bind: %#v", siwcDisk)
	}
}

func TestNextRefreshCheckAtIncludesSIWCWhenClassifiedAsAPIKey(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	siwcExp := now.Add(20 * time.Minute)
	auth := &Auth{
		ID:       "codex-hybrid-apikey.json",
		Provider: "codex",
		Attributes: map[string]string{
			AttributeAuthKind: AuthKindAPIKey,
			AttributeAPIKey:   "sk-legacy",
		},
		Metadata: map[string]any{
			"type":     "codex",
			"use_siwc": true,
			"siwc": map[string]any{
				"access_token":  "siwc-tok",
				"refresh_token": "siwc-ref",
				"client_id":     "oaiapp_hybrid",
				"expired":       siwcExp.Format(time.RFC3339),
			},
		},
	}
	if gotKind := auth.AuthKind(); gotKind != AuthKindOAuth {
		t.Fatalf("AuthKind(use_siwc=true) = %q, want %q", gotKind, AuthKindOAuth)
	}
	due, ok := nextRefreshCheckAt(now, auth, time.Minute)
	wantDue := siwcExp.Add(-CodexSIWCRefreshLead)
	if !ok || !due.Equal(wantDue) {
		t.Fatalf("nextRefreshCheckAt = (%v, %v), want (%v, true)", due, ok, wantDue)
	}
}
