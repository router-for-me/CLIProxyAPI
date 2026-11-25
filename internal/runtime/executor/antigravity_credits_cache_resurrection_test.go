package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAntigravityCreditsRemovedBalanceDoesNotResurrect(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if callCount.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"paidTier":{"id":"g1-pro-tier","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"100","minimumCreditAmountForUsage":"10"}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"paidTier":{"id":"g1-pro-tier","availableCredits":[]}}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		QuotaExceeded: config.QuotaExceeded{
			AntigravityCredits: true,
		},
	}
	exec := &AntigravityExecutor{cfg: cfg}
	auth := &cliproxyauth.Auth{
		ID:       "ag-credits-removed-1",
		Provider: "antigravity",
		Attributes: map[string]string{
			"base_url": srv.URL,
		},
		Metadata: map[string]any{
			"access_token": "tok-1",
		},
	}

	ctx := context.Background()

	// First refresh populates positive balance in cache and hint.
	exec.updateAntigravityCreditsBalance(ctx, auth, "tok-1")
	hasCredits, err := antigravityAuthHasCreditsRequired(ctx, auth)
	if err != nil {
		t.Fatalf("unexpected error after first refresh: %v", err)
	}
	if !hasCredits {
		t.Fatalf("expected credits available after first refresh")
	}

	// Second refresh observes GOOGLE_ONE_AI credits removed.
	exec.updateAntigravityCreditsBalance(ctx, auth, "tok-1")

	if _, exists := antigravityCreditsBalanceByAuth.Load(auth.ID); exists {
		t.Fatalf("stale positive credits balance remained in antigravityCreditsBalanceByAuth after GOOGLE_ONE_AI credits were removed")
	}

	// Simulate hint expiration/reset so antigravityAuthHasCreditsRequired falls back to cached balance.
	cliproxyauth.SetAntigravityCreditsHintForAuth(auth, cliproxyauth.AntigravityCreditsHint{})

	if _, err = antigravityAuthHasCreditsRequired(ctx, auth); err != nil {
		t.Fatalf("unexpected error after credits removal: %v", err)
	}
	if hint, ok := cliproxyauth.GetAntigravityCreditsHintForAuth(auth); ok && hint.Known && hint.CreditAmount > 0 {
		t.Fatalf("stale positive credits balance resurrected into hint after GOOGLE_ONE_AI credits were removed: %+v", hint)
	}
}

func TestAntigravityCreditsRemovedBalanceDoesNotResurrectHomeKV(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	fakeKV := &fakeAntigravityKVClient{values: make(map[string][]byte)}
	origKVClient := currentAntigravityKVClient
	currentAntigravityKVClient = func() (antigravityKVClient, bool, error) {
		return fakeKV, true, nil
	}
	t.Cleanup(func() {
		currentAntigravityKVClient = origKVClient
	})

	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if callCount.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"paidTier":{"id":"g1-pro-tier","availableCredits":[{"creditType":"GOOGLE_ONE_AI","creditAmount":"100","minimumCreditAmountForUsage":"10"}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"paidTier":{"id":"g1-pro-tier","availableCredits":[]}}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		QuotaExceeded: config.QuotaExceeded{
			AntigravityCredits: true,
		},
	}
	exec := &AntigravityExecutor{cfg: cfg}
	auth := &cliproxyauth.Auth{
		ID:       "ag-credits-removed-kv-1",
		Provider: "antigravity",
		Attributes: map[string]string{
			"base_url": srv.URL,
		},
		Metadata: map[string]any{
			"access_token": "tok-kv-1",
		},
	}

	ctx := context.Background()

	exec.updateAntigravityCreditsBalance(ctx, auth, "tok-kv-1")
	exec.updateAntigravityCreditsBalance(ctx, auth, "tok-kv-1")

	kvKey := antigravityCreditsBalanceKey(auth.ID, cliproxyauth.AntigravityCreditsScope(auth))
	if _, exists := fakeKV.values[kvKey]; exists {
		t.Fatalf("stale positive credits balance remained in Home KV after GOOGLE_ONE_AI credits were removed")
	}

	// Clear hint to force Home KV fallback.
	cliproxyauth.SetAntigravityCreditsHintForAuth(auth, cliproxyauth.AntigravityCreditsHint{})

	if _, err := antigravityAuthHasCreditsRequired(ctx, auth); err != nil {
		t.Fatalf("unexpected error after credits removal in home kv mode: %v", err)
	}
	if hint, ok := cliproxyauth.GetAntigravityCreditsHintForAuth(auth); ok && hint.Known && hint.CreditAmount > 0 {
		t.Fatalf("stale positive credits balance resurrected from Home KV after GOOGLE_ONE_AI credits were removed: %+v", hint)
	}
}

func TestAntigravityCreditsClearFailureStateDeletesCachedBalance(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	auth := &cliproxyauth.Auth{
		ID:       "ag-credits-clear-failure-1",
		Provider: "antigravity",
	}

	// Seed a stale positive balance in the in-process cache.
	storeAntigravityCreditsBalanceBestEffort(auth, antigravityCreditsBalance{
		CreditAmount:    100,
		MinCreditAmount: 10,
		PaidTierID:      "g1-pro-tier",
		Known:           true,
	})

	// Record a failure and mark hint unavailable.
	key, unlock, current := lockAntigravityCreditsScope(auth)
	if !current {
		t.Fatalf("expected current credits scope")
	}
	antigravityCreditsFailureByAuth.Store(key, antigravityCreditsFailureState{})
	unlock()
	cliproxyauth.SetAntigravityCreditsHintForAuth(auth, cliproxyauth.AntigravityCreditsHint{
		Known:     true,
		Available: false,
		UpdatedAt: time.Now(),
	})

	clearAntigravityCreditsFailureState(auth)

	if _, exists := antigravityCreditsBalanceByAuth.Load(key); exists {
		t.Fatalf("expected clearAntigravityCreditsFailureState to delete cached balance entry")
	}

	// Calling antigravityAuthHasCreditsRequired should NOT resurrect the stale 100-credit hint.
	_, err := antigravityAuthHasCreditsRequired(context.Background(), auth)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hint, ok := cliproxyauth.GetAntigravityCreditsHintForAuth(auth); ok && hint.Known && hint.CreditAmount > 0 {
		t.Fatalf("stale cached balance resurrected into hint: %+v", hint)
	}
}
