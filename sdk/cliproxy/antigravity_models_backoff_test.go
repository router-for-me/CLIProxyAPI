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

func TestAntigravityAuthAndForbiddenBackoffWindowsAndCap(t *testing.T) {
	policies := []struct {
		name    string
		next    func(antigravityFailureState, time.Time, func(int64) int64) antigravityFailureState
		windows []time.Duration
	}{
		{
			name:    "auth-401",
			next:    nextAntigravityAuthErrorFailure,
			windows: []time.Duration{10, 20, 30},
		},
		{
			name:    "forbidden-403",
			next:    nextAntigravityForbiddenFailure,
			windows: []time.Duration{10, 20, 40, 60},
		},
	}
	for _, policy := range policies {
		t.Run(policy.name, func(t *testing.T) {
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
					for i := range 50 {
						expectedWindow := policy.windows[min(i, len(policy.windows)-1)] * time.Minute
						state = policy.next(state, now, sample.random)
						delay := state.nextRetry.Sub(now)
						expected := expectedWindow/2 + time.Duration(sample.random(int64(expectedWindow/2)+1))
						wantFailures := uint8(min(i+1, int(antigravityFailureMaxCount)))
						if delay != expected || state.failures != wantFailures || !state.lastFailure.Equal(now) {
							t.Fatalf("attempt %d: state=%+v delay=%s want=%s wantFailures=%d", i+1, state, delay, expected, wantFailures)
						}
						now = state.nextRetry
					}
					state = policy.next(state, state.nextRetry.Add(time.Minute), sample.random)
					if state.failures != antigravityFailureMaxCount {
						t.Fatalf("expired retry deadline discarded failure history: got %d want %d", state.failures, antigravityFailureMaxCount)
					}
					state = policy.next(state, state.lastFailure.Add(2*antigravityCapabilityCacheTTL+time.Nanosecond), sample.random)
					if state.failures != 1 {
						t.Fatalf("inactive failure history did not reset: got %d want 1", state.failures)
					}
				})
			}
		})
	}
}

func TestAntigravityMixed401Then503PreservesTransientBackoffCap(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var state antigravityFailureState
	for range 4 {
		state = nextAntigravityAuthErrorFailure(state, now, func(int64) int64 { return 0 })
		now = state.nextRetry
	}
	if state.failures != 4 {
		t.Fatalf("after 4 auth errors failures=%d want 4", state.failures)
	}
	// A 5th failure that is a 503 transient error must reach the 5th-step 30m window (15m lower bound).
	state = nextAntigravityFailure(state, now, func(int64) int64 { return 0 })
	if state.failures != 5 {
		t.Fatalf("mixed 503 failures=%d want 5", state.failures)
	}
	if delay := state.nextRetry.Sub(now); delay != 15*time.Minute {
		t.Fatalf("mixed 503 delay=%s want 15m0s", delay)
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

func TestAntigravityForbiddenBackoffPersistsAcrossTokenRotationAndCapsAt60Minutes(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	antigravityNowFunc = func() time.Time { return now }
	t.Cleanup(func() { antigravityNowFunc = time.Now })

	var calls atomic.Int32
	var status atomic.Int32
	status.Store(http.StatusOK)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(`{"models":{"gemini-3.1-flash-lite":{}}}`))
	}))
	defer server.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{cfg: &config.Config{}, coreManager: manager}
	auth := antigravityTestAuth("forbidden-backoff", server.URL)
	auth, errRegister := manager.Register(t.Context(), auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })

	// Prime a successful catalog first to confirm 403 preserves it during backoff.
	svc.refreshAntigravityModels(t.Context())
	svc.WaitAntigravityProbes()
	if calls.Load() != 1 || len(GlobalModelRegistry().GetModelsForClient(auth.ID)) != 1 {
		t.Fatalf("initial registration failed: calls=%d models=%d", calls.Load(), len(GlobalModelRegistry().GetModelsForClient(auth.ID)))
	}

	status.Store(http.StatusForbidden)
	now = now.Add(antigravityCapabilityCacheTTL)
	expectedWindows := []time.Duration{10, 20, 40, 60, 60, 60}
	var lastNextRetry time.Time
	for i, mins := range expectedWindows {
		svc.refreshAntigravityModels(t.Context())
		svc.WaitAntigravityProbes()
		wantCalls := int32(i + 2)
		if calls.Load() != wantCalls {
			t.Fatalf("attempt %d: calls=%d want=%d", i+1, calls.Load(), wantCalls)
		}
		cacheKey := svc.antigravityCapabilityKey(auth)
		antigravityCapabilityMu.RLock()
		state, okKey := antigravityAuthFailureCache[cacheKey]
		entryCount := len(antigravityAuthFailureCache)
		antigravityCapabilityMu.RUnlock()
		if !okKey || entryCount != 1 {
			t.Fatalf("attempt %d: expected single cacheKey failure entry, ok=%v count=%d", i+1, okKey, entryCount)
		}
		window := mins * time.Minute
		delay := state.nextRetry.Sub(now)
		if delay < window/2 || delay > window {
			t.Fatalf("attempt %d: delay=%s want in [%s, %s]", i+1, delay, window/2, window)
		}
		if len(GlobalModelRegistry().GetModelsForClient(auth.ID)) != 1 {
			t.Fatalf("attempt %d: 403 discarded last successful catalog", i+1)
		}

		// Rotating access_token before nextRetry must NOT reset backoff or fire an immediate probe.
		rotated := auth.Clone()
		rotated.Metadata["access_token"] = "rotated-" + string(rune('a'+i))
		var errUpdate error
		auth, errUpdate = manager.Update(t.Context(), rotated)
		if errUpdate != nil {
			t.Fatal(errUpdate)
		}
		now = state.nextRetry.Add(-30 * time.Second)
		if gotDelay := svc.nextAntigravityModelRefreshDelay(); gotDelay != 30*time.Second {
			t.Fatalf("attempt %d: next refresh delay=%s want 30s", i+1, gotDelay)
		}
		svc.refreshAntigravityModels(t.Context())
		svc.WaitAntigravityProbes()
		svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
		if calls.Load() != wantCalls {
			t.Fatalf("attempt %d: token rotation bypassed 403 backoff (calls=%d want=%d)", i+1, calls.Load(), wantCalls)
		}
		lastNextRetry = state.nextRetry
		if i < len(expectedWindows)-1 {
			now = state.nextRetry
		}
	}

	// While the original route's 403 backoff is still active (now < lastNextRetry),
	// switching project_id changes cacheKey and must probe immediately.
	changedProject := auth.Clone()
	changedProject.Metadata["project_id"] = "project-changed"
	var errUpdateProject error
	changedProject, errUpdateProject = manager.Update(t.Context(), changedProject)
	if errUpdateProject != nil {
		t.Fatal(errUpdateProject)
	}
	status.Store(http.StatusOK)
	beforeProjectCalls := calls.Load()
	svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), changedProject)
	if calls.Load() != beforeProjectCalls+1 {
		t.Fatalf("project change blocked by active original route 403 backoff: calls=%d want=%d", calls.Load(), beforeProjectCalls+1)
	}

	// Switching back to the original route before lastNextRetry must remain throttled.
	restored := changedProject.Clone()
	restored.Metadata["project_id"] = "project-forbidden-backoff"
	var errRestore error
	auth, errRestore = manager.Update(t.Context(), restored)
	if errRestore != nil {
		t.Fatal(errRestore)
	}
	beforeOriginalBlockedCalls := calls.Load()
	svc.refreshAntigravityModels(t.Context())
	svc.WaitAntigravityProbes()
	svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
	if calls.Load() != beforeOriginalBlockedCalls {
		t.Fatalf("original route probed before its 403 backoff expired: calls=%d want=%d", calls.Load(), beforeOriginalBlockedCalls)
	}

	// Once the original route's backoff expires, recovery succeeds and clears cacheKey failure state.
	now = lastNextRetry
	svc.refreshAntigravityModels(t.Context())
	svc.WaitAntigravityProbes()
	antigravityCapabilityMu.RLock()
	_, stillFailed := antigravityAuthFailureCache[svc.antigravityCapabilityKey(auth)]
	antigravityCapabilityMu.RUnlock()
	if stillFailed {
		t.Fatal("successful recovery did not clear 403 cacheKey failure state")
	}
}

func TestAntigravityAuthError401UsesLongerBackoffAndUnblocksOnTokenRotation(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	antigravityNowFunc = func() time.Time { return now }
	t.Cleanup(func() { antigravityNowFunc = time.Now })

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") == "Bearer token-auth-401" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":{"gemini-3.1-flash-lite":{}}}`))
	}))
	defer server.Close()

	svc := &Service{cfg: &config.Config{}}
	auth := antigravityTestAuth("auth-401", server.URL)
	svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
	if calls.Load() != 1 {
		t.Fatalf("calls=%d want 1", calls.Load())
	}
	antigravityCapabilityMu.RLock()
	if len(antigravityAuthFailureCache) != 1 {
		antigravityCapabilityMu.RUnlock()
		t.Fatalf("failure entries=%d want 1", len(antigravityAuthFailureCache))
	}
	var state antigravityFailureState
	for _, entry := range antigravityAuthFailureCache {
		state = entry
	}
	antigravityCapabilityMu.RUnlock()
	delay := state.nextRetry.Sub(now)
	if delay < 5*time.Minute || delay > 10*time.Minute {
		t.Fatalf("401 first backoff delay=%s want in [5m, 10m]", delay)
	}

	// Same rejected token remains throttled.
	svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
	if calls.Load() != 1 {
		t.Fatalf("same 401 token was not throttled: calls=%d", calls.Load())
	}

	// Rotating access_token after 401 must immediately retry and recover.
	rotated := auth.Clone()
	rotated.Metadata["access_token"] = "refreshed-token"
	hints := svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), rotated)
	if calls.Load() != 2 || len(hints.ModelIDs) != 1 {
		t.Fatalf("rotated token after 401 did not recover immediately: calls=%d hints=%v", calls.Load(), hints)
	}
}

func TestAntigravityDisabledAuthSkipsModelCapabilityProbe(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*coreauth.Auth)
	}{
		{
			name:   "DisabledFlag",
			mutate: func(a *coreauth.Auth) { a.Disabled = true },
		},
		{
			name:   "StatusDisabled",
			mutate: func(a *coreauth.Auth) { a.Status = coreauth.StatusDisabled },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetAntigravityCapabilityCache()
			t.Cleanup(resetAntigravityCapabilityCache)
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			antigravityNowFunc = func() time.Time { return now }
			t.Cleanup(func() { antigravityNowFunc = time.Now })

			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusForbidden)
			}))
			defer server.Close()

			manager := coreauth.NewManager(nil, nil, nil)
			svc := &Service{cfg: &config.Config{}, coreManager: manager}
			auth := antigravityTestAuth("disabled-"+tc.name, server.URL)
			auth.Metadata[antigravityPersistedHintsMetaKey] = buildPersistedAntigravityCapabilityHintsValue(
				"project-disabled-"+tc.name, server.URL,
				antigravityModelCapabilityHints{ModelIDs: map[string]struct{}{"gemini-3.1-flash-lite": {}}},
			)
			tc.mutate(auth)
			registered, errRegister := manager.Register(t.Context(), auth)
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(registered.ID) })

			// Pre-seed a short retry deadline so nextAntigravityModelRefreshDelay would shorten if it failed to skip.
			cacheKey := svc.antigravityCapabilityKey(registered)
			antigravityCapabilityMu.Lock()
			antigravityAuthFailureCache[cacheKey] = antigravityFailureState{
				failures:    1,
				lastFailure: now,
				nextRetry:   now.Add(10 * time.Second),
			}
			antigravityCapabilityMu.Unlock()

			wake := svc.antigravityModelRefreshWake()
			select {
			case <-wake:
			default:
			}

			gotKey, gotHints := svc.fetchAntigravityModelCapabilityHints(t.Context(), registered)
			if gotKey != "" || gotHints.ModelIDs != nil {
				t.Fatalf("disabled auth did not short-circuit: key=%q hints=%v", gotKey, gotHints)
			}
			select {
			case <-wake:
				t.Fatal("disabled auth fetch triggered refresh wakeup")
			default:
			}

			// Standalone Service without coreManager must also short-circuit without seeding cache.
			standaloneSvc := &Service{cfg: &config.Config{}}
			gotKey, gotHints = standaloneSvc.fetchAntigravityModelCapabilityHints(t.Context(), registered)
			if gotKey != "" || gotHints.ModelIDs != nil {
				t.Fatalf("standalone disabled auth did not short-circuit: key=%q hints=%v", gotKey, gotHints)
			}
			antigravityCapabilityMu.RLock()
			_, seeded := antigravityCapabilityCache[cacheKey]
			antigravityCapabilityMu.RUnlock()
			if seeded {
				t.Fatal("disabled auth seeded capability cache")
			}

			svc.refreshAntigravityModels(t.Context())
			svc.WaitAntigravityProbes()
			if delay := svc.nextAntigravityModelRefreshDelay(); delay != antigravityRefreshScanInterval {
				t.Fatalf("nextAntigravityModelRefreshDelay=%s want %s (disabled auth must be skipped)", delay, antigravityRefreshScanInterval)
			}
			if calls.Load() != 0 {
				t.Fatalf("disabled auth issued %d network probes, want 0", calls.Load())
			}
		})
	}
}
