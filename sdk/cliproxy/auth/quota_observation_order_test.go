package auth

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

func TestQuotaStateObserveResponseHeadersIgnoresOlderSnapshot(t *testing.T) {
	newer, older := time.Unix(2_000, 0), time.Unix(1_000, 0)
	var quota QuotaState
	quota.ObserveResponseHeadersForProvider("claude", http.Header{"Anthropic-Ratelimit-Unified-5h-Status": {"rejected"}}, newer)
	if quota.ObserveResponseHeadersForProvider("claude", http.Header{"Anthropic-Ratelimit-Unified-5h-Status": {"allowed"}}, older) {
		t.Fatal("an older snapshot replaced a newer one")
	}
	if got := quota.Signals["Anthropic-Ratelimit-Unified-5h-Status"]; got != "rejected" || !quota.ObservedAt.Equal(newer) {
		t.Fatalf("snapshot = %q at %v, want rejected at %v", got, quota.ObservedAt, newer)
	}
}

func TestAntigravityQuotaLookupSurvivesResponseAndObservationMerge(t *testing.T) {
	lookedUpAt := time.Unix(2_000, 0)
	window := QuotaWindow{Key: "ag_gemini", Scoped: true}
	manager := NewManager(nil, nil, nil)
	auth, err := manager.Register(t.Context(), &Auth{
		ID: "quota-antigravity", Provider: "antigravity", Status: StatusActive,
		Quota: QuotaState{LookedUpAt: lookedUpAt, LookupWindows: []QuotaWindow{window}},
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.MarkResult(t.Context(), Result{AuthID: auth.ID, Provider: auth.Provider, Model: "gemini-pro", Success: true})
	stored, ok := manager.GetByID(auth.ID)
	if !ok || !stored.Quota.LookedUpAt.Equal(lookedUpAt) || len(stored.Quota.LookupWindows) != 1 || stored.Quota.LookupWindows[0] != window {
		t.Fatalf("response changed the lookup snapshot: %+v", stored)
	}

	stale := QuotaState{
		LookedUpAt: lookedUpAt.Add(-time.Second), LookupWindows: []QuotaWindow{{Key: "stale"}},
		Exceeded: true,
	}
	applyObservationFields(&stale, mergeQuotaObservation(stale, stored.Quota))
	if !stale.LookedUpAt.Equal(lookedUpAt) || len(stale.LookupWindows) != 1 || stale.LookupWindows[0] != window || !stale.Exceeded {
		t.Fatalf("expected complete newer snapshot and preserved cooldown, got %+v", stale)
	}
	stale.LookupWindows[0].Key = "modified"
	if stored.Quota.LookupWindows[0] != window {
		t.Fatal("merged lookup windows alias the source")
	}
}

// A stream whose headers arrived before the credential's 429 finishes after it. Its success must
// not restore the pre-limit snapshot, or the spent pool stops answering as a usage limit and
// clients lose the reset time.
func TestMarkResult_StreamFinishingAfterLimitKeepsRejectedSnapshot(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := registerExhaustionAuth(t, &Auth{ID: "order-claude", Provider: "claude", Status: StatusActive})
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register: %v", errRegister)
	}
	reset := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	windowHeaders := func(status, utilization string) http.Header {
		return http.Header{
			"Anthropic-Ratelimit-Unified-5h-Status":      {status},
			"Anthropic-Ratelimit-Unified-5h-Utilization": {utilization},
			"Anthropic-Ratelimit-Unified-5h-Reset":       {reset},
		}
	}

	streamCtx := internallogging.WithFreshResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(streamCtx, windowHeaders("allowed_warning", "0.98"))
	// Distinct arrival instants even on coarse clocks.
	time.Sleep(time.Millisecond)

	limitCtx := internallogging.WithFreshResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(limitCtx, windowHeaders("rejected", "1.0"))
	manager.MarkResult(limitCtx, Result{
		AuthID: auth.ID, Provider: "claude", Model: exhaustionTestModel,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: `{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit."}}`},
	})
	manager.MarkResult(streamCtx, Result{AuthID: auth.ID, Provider: "claude", Model: exhaustionTestModel, Success: true})

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("auth missing")
	}
	if got := updated.Quota.Signals["Anthropic-Ratelimit-Unified-5h-Status"]; got != "rejected" {
		t.Fatalf("5h status = %q, want rejected", got)
	}
	if _, exhausted := credentialQuotaLimit(updated, exhaustionTestModel, time.Now()); !exhausted {
		t.Fatal("credential no longer reads as exhausted")
	}
}
