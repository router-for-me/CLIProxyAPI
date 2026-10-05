package cliproxy

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestAntigravityFailureBackoffWindowsAndCap(t *testing.T) {
	for _, sample := range []struct {
		name   string
		random func(int64) int64
	}{
		{"lower", func(int64) int64 { return 0 }},
		{"middle", func(n int64) int64 { return n / 2 }},
		{"upper", func(n int64) int64 { return n - 1 }},
	} {
		t.Run(sample.name, func(t *testing.T) {
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			var state antigravityFailureState
			for i := range 300 {
				expectedWindow := []time.Duration{2, 4, 8, 16, 30}[min(i, 4)] * time.Minute
				state = nextAntigravityFailure(state, now, sample.random)
				delay := state.nextRetry.Sub(now)
				expected := expectedWindow/2 + time.Duration(sample.random(int64(expectedWindow/2)+1))
				if delay != expected || state.failures != uint8(min(i+1, 5)) || !state.lastFailure.Equal(now) {
					t.Fatalf("attempt %d: state=%+v delay=%s want=%s", i+1, state, delay, expected)
				}
				now = state.nextRetry
			}
			// An expired retry deadline alone must not reset consecutive failures.
			state = nextAntigravityFailure(state, state.nextRetry.Add(time.Minute), sample.random)
			if state.failures != 5 {
				t.Fatal("expired retry deadline discarded failure history")
			}
			// Truly inactive account/token histories can start over after retention.
			state = nextAntigravityFailure(state, state.lastFailure.Add(2*antigravityCapabilityCacheTTL+time.Nanosecond), sample.random)
			if state.failures != 1 {
				t.Fatal("inactive failure history did not reset")
			}
		})
	}
}

func TestAntigravityFailureBackoffIntegration(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	antigravityNowFunc = func() time.Time { return now }
	t.Cleanup(func() { antigravityNowFunc = time.Now })
	var calls atomic.Int32
	var status atomic.Int32
	status.Store(503)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(`{"models":{"gemini-3.1-flash-lite":{}}}`))
	}))
	defer server.Close()
	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{cfg: &config.Config{}, coreManager: manager}
	auth := antigravityTestAuth("backoff", server.URL)
	auth, _ = manager.Register(t.Context(), auth)
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
	readFailure := func() antigravityFailureState {
		t.Helper()
		antigravityCapabilityMu.RLock()
		defer antigravityCapabilityMu.RUnlock()
		if len(antigravityAuthFailureCache) != 1 {
			t.Fatalf("failure entries=%d", len(antigravityAuthFailureCache))
		}
		for _, entry := range antigravityAuthFailureCache {
			return entry
		}
		return antigravityFailureState{}
	}
	antigravityCapabilityMu.Lock()
	antigravityAuthFailureCache["inactive-token"] = antigravityFailureState{
		failures: 5, lastFailure: now.Add(-2*antigravityCapabilityCacheTTL - time.Nanosecond),
	}
	antigravityCapabilityMu.Unlock()
	for i := range 8 {
		svc.refreshAntigravityModels(t.Context())
		svc.WaitAntigravityProbes()
		if calls.Load() != int32(i+1) {
			t.Fatalf("attempt %d calls=%d", i+1, calls.Load())
		}
		state := readFailure()
		window := []time.Duration{2, 4, 8, 16, 30}[min(i, 4)] * time.Minute
		delay := state.nextRetry.Sub(now)
		if state.failures != uint8(min(i+1, 5)) || delay < window/2 || delay > window {
			t.Fatalf("attempt %d: %+v delay=%s", i+1, state, delay)
		}
		now = state.nextRetry.Add(-time.Nanosecond)
		// Both periodic and direct registration fetches respect the same deadline.
		svc.refreshAntigravityModels(t.Context())
		svc.WaitAntigravityProbes()
		svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
		if calls.Load() != int32(i+1) {
			t.Fatal("retried before jittered deadline")
		}
		now = state.nextRetry
	}
	status.Store(200)
	svc.refreshAntigravityModels(t.Context())
	svc.WaitAntigravityProbes()
	antigravityCapabilityMu.RLock()
	failures := len(antigravityAuthFailureCache)
	antigravityCapabilityMu.RUnlock()
	if failures != 0 {
		t.Fatal("success did not reset consecutive failures")
	}
	if len(GlobalModelRegistry().GetModelsForClient(auth.ID)) != 1 {
		t.Fatal("successful retry did not restore models")
	}
	status.Store(503)
	now = now.Add(antigravityCapabilityCacheTTL)
	svc.refreshAntigravityModels(t.Context())
	svc.WaitAntigravityProbes()
	if state := readFailure(); state.failures != 1 {
		t.Fatal("failure following success did not restart at first window")
	}
	if len(GlobalModelRegistry().GetModelsForClient(auth.ID)) != 1 {
		t.Fatal("failed refresh discarded last successful catalog")
	}
	// A new token must not inherit the old token's authorization/backoff state.
	rotated := auth.Clone()
	rotated.Metadata["access_token"] = "rotated"
	rotated, _ = manager.Update(t.Context(), rotated)
	before := calls.Load()
	svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), rotated)
	if calls.Load() != before+1 {
		t.Fatal("new token blocked by old token backoff")
	}
	antigravityCapabilityMu.RLock()
	defer antigravityCapabilityMu.RUnlock()
	if len(antigravityAuthFailureCache) != 2 {
		t.Fatal("token histories not isolated")
	}
	for _, failure := range antigravityAuthFailureCache {
		if failure.failures != 1 {
			t.Fatal("new token inherited consecutive failures")
		}
	}
}
