package clawmaster

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// The usage poll is advisory. On 2026-10-11 every profile polled at weekly_all 100 and the pool refused
// every request with a synthesized 429, while a native Claude Code session on the same Anthropic
// account as one of those profiles kept being served. Only Anthropic's answer to a real request (a 429
// with unified-status rejected) takes a subscription out; the poll orders candidates and nothing more.

// rejectByAnthropic is what a real 429 with anthropic-ratelimit-unified-status: rejected does to the
// selector: a credential-scoped rejection that holds for retryAfter.
func rejectByAnthropic(selector *backendSeriesSelector, authID string, retryAfter time.Duration) {
	result := backendSeriesQuotaResult(authID)
	result.RetryAfter = &retryAfter
	selector.OnResult(result)
}

// exhaustByAnthropic is a used-up week as the pool learns it now: the poll at 100% AND Anthropic's
// rejection of a real request until the weekly reset.
func exhaustByAnthropic(selector *backendSeriesSelector, authID string, reset time.Time) {
	selector.observeQuota(authID, backendWeeklyQuota{known: true, used: 1, resetsAt: reset})
	rejectByAnthropic(selector, authID, reset.Sub(selector.nowOrReal()))
}

// liftRejection is Anthropic's retry-after having passed.
func liftRejection(selector *backendSeriesSelector, authID string) {
	selector.mu.Lock()
	delete(selector.quotaBlockedUntil, authID)
	selector.mu.Unlock()
}

// (1) A polled 100% does not refuse: the request goes to Anthropic, end to end through the manager,
// and Anthropic's acceptance is logged once per profile per band, not per request.
func TestPolledExhaustionStillSendsTheRequestToAnthropic(t *testing.T) {
	logs := captureLogs(t, "info")
	first, second := t.Name()+"-first", t.Name()+"-second"
	reset := time.Now().Add(48 * time.Hour)
	selector := &backendSeriesSelector{authIDs: []string{first, second}, provider: "claude", names: map[string]string{first: "claude-tertiary", second: "claude-fourth"}}
	t.Cleanup(selector.Stop)
	selector.observeQuota(first, backendWeeklyQuota{known: true, used: 1, resetsAt: reset})
	selector.observeQuota(second, backendWeeklyQuota{known: true, used: 1, resetsAt: reset.Add(time.Hour)})
	capture := &backendSeriesCapture{}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.SetRetryConfig(0, 0, 2)
	setBackendSeriesResultPolicy(manager, selector)
	manager.RegisterExecutor(capture)
	for _, authID := range []string{first, second} {
		registry.GetGlobalRegistry().RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: backendAuthSelectionModel}})
		if _, err := manager.Register(t.Context(), &coreauth.Auth{ID: authID, Provider: "claude", Status: coreauth.StatusActive}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	}
	handler := newBackendHandler(t.Context(), BackendOptions{Provider: "claude", UseRequestModel: true}, handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager))
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-test","messages":[{"role":"user","content":"hello"}]}`))
		r.Header.Set("X-Claude-Code-Session-Id", "polled-full")
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d at a polled 100%% was refused: status=%d body=%s", i+1, w.Code, w.Body.String())
		}
	}
	capture.mu.Lock()
	calls := strings.Join(capture.calls, ",")
	capture.mu.Unlock()
	if calls != first+","+first+","+first {
		t.Fatalf("upstream calls = %s, want every request on the earliest-reset subscription", calls)
	}
	out := logs.String()
	if strings.Contains(out, "no inference account could be chosen") {
		t.Fatalf("a polled 100%% was logged as a refusal:\n%s", out)
	}
	if got := strings.Count(out, "Anthropic accepted a request on a subscription the usage poll calls exhausted"); got != 1 {
		t.Fatalf("acceptance above the poll logged %d times, want once per band:\n%s", got, out)
	}
	if !strings.Contains(out, "profile=claude-tertiary") {
		t.Fatalf("acceptance line does not name the profile:\n%s", out)
	}

	// A new band (the poll drops, then climbs back) logs it again once.
	selector.observeQuota(first, backendWeeklyQuota{known: true, used: 0.5, resetsAt: reset.Add(7 * 24 * time.Hour)})
	selector.observeQuota(first, backendWeeklyQuota{known: true, used: 0.95, resetsAt: reset.Add(7 * 24 * time.Hour)})
	selector.OnResult(coreauth.Result{AuthID: first, Provider: "claude", Success: true})
	selector.OnResult(coreauth.Result{AuthID: first, Provider: "claude", Success: true})
	if got := strings.Count(logs.String(), "the usage poll calls reserve"); got != 1 {
		t.Fatalf("acceptance in the reserve band logged %d times, want once:\n%s", got, logs.String())
	}
}

func TestPolledExhaustionDoesNotRefuseOrMoveAtTheSelector(t *testing.T) {
	selector, auths, reset := namedSelector(t)
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.5, resetsAt: reset})
	selector.observeQuota("profile-b", backendWeeklyQuota{known: true, used: 0.2, resetsAt: reset.Add(24 * time.Hour)})
	requireBackendSeriesPickWith(t, selector, plainRequest("bound"), auths, "profile-a")
	// Reserve, then 100%: the conversation stays (no poll-driven move) and is not refused.
	for _, used := range []float64{0.95, 1} {
		selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: used, resetsAt: reset})
		requireBackendSeriesPickWith(t, selector, plainRequest("bound"), auths, "profile-a")
	}
	// Opaque state on a polled-exhausted subscription is served too.
	opaque := plainRequest("bound")
	opaque.OriginalRequest = []byte(`{"messages":[{"role":"assistant","content":[{"type":"compaction","content":"summary","signature":"opaque"}]}]}`)
	requireBackendSeriesPickWith(t, selector, opaque, auths, "profile-a")
	// With every subscription polled at 100% a new conversation still gets one.
	selector.observeQuota("profile-b", backendWeeklyQuota{known: true, used: 1, resetsAt: reset.Add(24 * time.Hour)})
	requireBackendSeriesPickWith(t, selector, plainRequest("new"), auths, "profile-a")
	selector.mu.Lock()
	exhausted := selector.subscriptionsExhaustedLocked()
	selector.mu.Unlock()
	if exhausted {
		t.Fatal("a polled 100% counted as exhausted, which would spend the paid backup")
	}
}

// A polled 100% never hands work to the paid API-key backup: only real rejections do.
func TestPolledExhaustionDoesNotSpendTheAPIBackup(t *testing.T) {
	selector, auths, reset := backendAPIBackupTestSelector(t)
	for _, id := range selector.authIDs {
		selector.observeQuota(id, backendWeeklyQuota{known: true, used: 1, resetsAt: reset})
	}
	requireBackendAPIBackupPick(t, selector, backendAPIBackupTestOptions("polled-only", false), auths, "subscription-a")
	for _, id := range selector.authIDs {
		rejectByAnthropic(selector, id, time.Hour)
	}
	requireBackendAPIBackupPick(t, selector, backendAPIBackupTestOptions("rejected", false), auths, "api-backup")
}

// (2) Anthropic's rejection of a real request holds the subscription until the time Anthropic gave:
// a conversation moves off it, opaque state bound to it gets Anthropic's 429 back with that time, and
// when every subscription is rejected the pool answers Anthropic's 429 at the earliest return.
func TestARealRejectionHoldsTheSubscriptionUntilAnthropicsReset(t *testing.T) {
	logs := captureLogs(t, "info")
	selector, auths, reset := namedSelector(t)
	now := reset.Add(-48 * time.Hour)
	selector.now = func() time.Time { return now }
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 1, resetsAt: reset})
	selector.observeQuota("profile-b", backendWeeklyQuota{known: true, used: 1, resetsAt: reset.Add(time.Hour)})
	requireBackendSeriesPickWith(t, selector, plainRequest("conversation"), auths, "profile-a")
	rejectByAnthropic(selector, "profile-a", 2*time.Hour)

	requireBackendSeriesPickWith(t, selector, plainRequest("conversation"), auths, "profile-b")
	requireBackendSeriesPickWith(t, selector, plainRequest("another"), auths, "profile-b")
	if out := logs.String(); !strings.Contains(out, "inference account switched") || !strings.Contains(out, "reason=weekly_exhausted") {
		t.Fatalf("the move on a real rejection was not logged as weekly_exhausted:\n%s", out)
	}

	// Opaque state bound to the rejected subscription: Anthropic's 429 until Anthropic's time.
	opaque := plainRequest("opaque-on-a")
	requireBackendSeriesPickWith(t, selector, plainRequest("opaque-on-a"), auths, "profile-b") // binds to b
	opaqueID, _ := backendSeriesSessionIDs(opaque)
	selector.mu.Lock()
	selector.sessions.Set(opaqueID, "profile-a")
	selector.mu.Unlock()
	opaque.OriginalRequest = []byte(`{"messages":[{"role":"assistant","content":[{"type":"compaction","content":"summary","signature":"opaque"}]}]}`)
	_, err := selector.Pick(t.Context(), "claude", "", opaque, auths)
	var refusal *backendRefusalError
	if !errors.As(err, &refusal) || refusal.kind != backendRefusalQuota || !refusal.resetAt.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("opaque state on a rejected subscription = %v, want Anthropic's 429 until the rejection ends", err)
	}

	// Every subscription rejected, no backup: Anthropic's 429 with the earliest return.
	rejectByAnthropic(selector, "profile-b", 5*time.Hour)
	_, err = selector.Pick(t.Context(), "claude", "", plainRequest("third"), auths)
	if !errors.As(err, &refusal) || refusal.kind != backendRefusalQuota || !refusal.resetAt.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("all rejected = %v, want a quota refusal at the earliest rejection end", err)
	}
	if !strings.Contains(err.Error(), "rate limited by Anthropic") {
		t.Fatalf("all-rejected reason = %q, want it to say Anthropic rate limited them", err.Error())
	}

	// Anthropic's time passes: the subscription is asked again, even though the poll still says 100%.
	now = now.Add(2 * time.Hour)
	requireBackendSeriesPickWith(t, selector, plainRequest("fourth"), auths, "profile-a")
}

// (3) After a rejection ends, a success clears it: the backoff resets and "available again" is logged.
// A success that was already in flight when the rejection arrived does not cancel the newer rejection.
func TestASuccessAfterTheRejectionClearsIt(t *testing.T) {
	logs := captureLogs(t, "info")
	selector, auths, reset := namedSelector(t)
	now := reset.Add(-48 * time.Hour)
	selector.now = func() time.Time { return now }
	selector.OnResult(backendSeriesQuotaResult("profile-a")) // no retry-after: the exponential backoff
	selector.OnResult(coreauth.Result{AuthID: "profile-a", Provider: "claude", Success: true})
	selector.mu.Lock()
	blocked := selector.quotaBlockedLocked("profile-a")
	selector.mu.Unlock()
	if !blocked {
		t.Fatal("an in-flight success cancelled a newer rejection")
	}
	requireBackendSeriesPickWith(t, selector, plainRequest("x"), auths, "profile-b")
	now = now.Add(time.Minute)
	requireBackendSeriesPickWith(t, selector, plainRequest("y"), auths, "profile-a")
	selector.OnResult(coreauth.Result{AuthID: "profile-a", Provider: "claude", Success: true})
	selector.mu.Lock()
	_, stillBlocked := selector.quotaBlockedUntil["profile-a"]
	level := selector.quotaBackoffLevel["profile-a"]
	selector.mu.Unlock()
	if stillBlocked || level != 0 {
		t.Fatalf("success after the rejection did not clear it: blocked=%v backoff=%d", stillBlocked, level)
	}
	if !strings.Contains(logs.String(), "profile available again") {
		t.Fatalf("recovery was not logged:\n%s", logs.String())
	}
}

// (4) The poll orders candidates: ok, then unknown, then reserve, then what the poll calls exhausted;
// within a band the earliest weekly reset first. A rejected subscription is not a candidate at all.
func TestThePollOrdersCandidatesWithoutExcludingAny(t *testing.T) {
	ids := []string{"exhausted-early", "reserve", "unknown", "ok-late", "ok-early", "rejected"}
	selector := &backendSeriesSelector{authIDs: ids, provider: "claude"}
	t.Cleanup(selector.Stop)
	reset := time.Date(2026, time.October, 12, 0, 0, 0, 0, time.UTC)
	selector.now = func() time.Time { return reset.Add(-24 * time.Hour) }
	selector.observeQuota("exhausted-early", backendWeeklyQuota{known: true, used: 1, resetsAt: reset})
	selector.observeQuota("reserve", backendWeeklyQuota{known: true, used: 0.95, resetsAt: reset})
	selector.observeQuota("ok-late", backendWeeklyQuota{known: true, used: 0.1, resetsAt: reset.Add(48 * time.Hour)})
	selector.observeQuota("ok-early", backendWeeklyQuota{known: true, used: 0.7, resetsAt: reset.Add(24 * time.Hour)})
	selector.observeQuota("rejected", backendWeeklyQuota{known: true, used: 0, resetsAt: reset})
	rejectByAnthropic(selector, "rejected", time.Hour)
	selector.mu.Lock()
	ranked := selector.rankedCandidatesLocked(nil, "", false)
	selector.mu.Unlock()
	got := make([]string, 0, len(ranked))
	for _, c := range ranked {
		got = append(got, c.auth.ID)
	}
	if want := "ok-early,ok-late,unknown,reserve,exhausted-early"; strings.Join(got, ",") != want {
		t.Fatalf("candidate order = %s, want %s", strings.Join(got, ","), want)
	}
	auths := make([]*coreauth.Auth, 0, len(ids))
	for _, id := range ids {
		auths = append(auths, backendSeriesTestAuth(id, "claude"))
	}
	requireBackendSeriesPick(t, selector, "claude", auths, "ok-early")
	// Anthropic rejects every other one: the polled-full subscription still takes the request.
	for _, id := range []string{"ok-early", "ok-late", "unknown", "reserve"} {
		rejectByAnthropic(selector, id, time.Hour)
	}
	requireBackendSeriesPick(t, selector, "claude", auths, "exhausted-early")
}

// A request whose preferred subscription already failed in this request is not an "exhausted" refusal:
// the session gets Anthropic's own error, and the log does not claim a refusal that never happened.
func TestAFailedAttemptIsNotReportedAsExhaustion(t *testing.T) {
	logs := captureLogs(t, "info")
	selector, auths, reset := namedSelector(t)
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 1, resetsAt: reset})
	selector.observeQuota("profile-b", backendWeeklyQuota{known: true, used: 1, resetsAt: reset.Add(time.Hour)})
	ctx := withBackendAttempt(t.Context())
	result := coreauth.Result{AuthID: "profile-a", Model: "claude-test", Error: &coreauth.Error{HTTPStatus: http.StatusInternalServerError}}
	recordBackendAttempt(ctx, result)
	selector.OnResult(result)
	opts := coreexecutor.Options{OriginalRequest: []byte(`{"messages":[{"role":"user","content":"hello"}]}`), Metadata: map[string]any{coreexecutor.RequestedModelMetadataKey: "claude-test"}}
	_, err := selector.Pick(ctx, "claude", "", opts, auths[1:])
	var refusal *backendRefusalError
	if !errors.As(err, &refusal) || refusal.kind == backendRefusalQuota || !refusal.relayed || strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("failed attempt refusal = %#v (%v), want a relayed non-quota refusal", refusal, err)
	}
	if strings.Contains(logs.String(), "no inference account could be chosen") {
		t.Fatalf("a relayed upstream error was logged as a refusal:\n%s", logs.String())
	}
}
