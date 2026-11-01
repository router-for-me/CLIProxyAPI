package auth

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func soonestResetClaudeHeaders(weeklyReset time.Time, weeklyStatus string) http.Header {
	reset := strconv.FormatInt(weeklyReset.Unix(), 10)
	return http.Header{
		"Anthropic-Ratelimit-Unified-7d-Reset":       []string{reset},
		"Anthropic-Ratelimit-Unified-7d-Status":      []string{weeklyStatus},
		"Anthropic-Ratelimit-Unified-7d-Utilization": []string{"0.9"},
		"Anthropic-Ratelimit-Unified-Overage-Status": []string{"rejected"},
		"Anthropic-Ratelimit-Unified-Reset":          []string{reset},
		"Anthropic-Ratelimit-Unified-Status":         []string{weeklyStatus},
	}
}

func soonestResetCodexHeaders(weeklyReset time.Time) http.Header {
	return http.Header{
		"X-Codex-Primary-Used-Percent":     []string{"12"},
		"X-Codex-Primary-Window-Minutes":   []string{"300"},
		"X-Codex-Secondary-Used-Percent":   []string{"40"},
		"X-Codex-Secondary-Window-Minutes": []string{"10080"},
		"X-Codex-Secondary-Reset-At":       []string{strconv.FormatInt(weeklyReset.Unix(), 10)},
	}
}

// TestManagerSoonestResetRoutesByObservedQuota drives the selector through the Manager
// with fake executors: each response carries rate-limit headers, the Manager records them
// as the credential's quota snapshot, and the next pick follows the snapshot. Fresh
// credentials are discovered by their probes, without any warm-up.
func TestManagerSoonestResetRoutesByObservedQuota(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, mode := range []string{"single-provider", "mixed-providers", "session-affinity", "session-affinity-mixed-providers"} {
		t.Run(mode, func(t *testing.T) {
			affinity := mode == "session-affinity" || mode == "session-affinity-mixed-providers"
			mixed := mode == "mixed-providers" || mode == "session-affinity-mixed-providers"

			ctx := context.Background()
			model := "claude-soonest-reset-" + mode
			var selector Selector = &SoonestResetSelector{}
			if affinity {
				affinitySelector := NewSessionAffinitySelector(selector)
				t.Cleanup(affinitySelector.Stop)
				selector = affinitySelector
			}
			manager := NewManager(nil, selector, nil)
			manager.SetRetryConfig(0, 0, 0)

			now := time.Now()
			prefix := "soonest-reset-" + mode + "-"
			idA, idB, idC, idD := prefix+"a", prefix+"b", prefix+"c", prefix+"d"
			var mu sync.Mutex
			headers := map[string]http.Header{
				idA: soonestResetClaudeHeaders(now.Add(96*time.Hour), "allowed"),
				idB: soonestResetClaudeHeaders(now.Add(24*time.Hour), "allowed"),
				idC: soonestResetClaudeHeaders(now.Add(48*time.Hour), "allowed_warning"),
				idD: soonestResetCodexHeaders(now.Add(72 * time.Hour)),
			}
			var attempts []string
			execute := func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				mu.Lock()
				attempts = append(attempts, auth.ID)
				responseHeaders := headers[auth.ID].Clone()
				mu.Unlock()
				internallogging.SetResponseHeaders(ctx, responseHeaders)
				return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
			}
			manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: "claude", executeFn: execute})

			providers := []string{"claude"}
			candidates := []*Auth{
				{ID: idA, Provider: "claude", Status: StatusActive},
				{ID: idB, Provider: "claude", Status: StatusActive},
				{ID: idC, Provider: "claude", Status: StatusActive},
			}
			if mixed {
				providers = append(providers, "codex")
				manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: "codex", executeFn: execute})
				candidates = append(candidates, &Auth{ID: idD, Provider: "codex", Status: StatusActive})
			}
			for _, candidate := range candidates {
				registry.GetGlobalRegistry().RegisterClient(candidate.ID, candidate.Provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(candidate.ID) })
				if _, errRegister := manager.Register(ctx, candidate); errRegister != nil {
					t.Fatalf("Register(%s) error = %v", candidate.ID, errRegister)
				}
			}

			options := func(session string) cliproxyexecutor.Options {
				opts := cliproxyexecutor.Options{Metadata: map[string]any{}}
				if session != "" {
					opts.Headers = http.Header{"X-Session-Id": []string{session}}
				}
				return opts
			}
			assertRoute := func(label string, opts cliproxyexecutor.Options, wantID string) {
				t.Helper()
				mu.Lock()
				before := len(attempts)
				mu.Unlock()
				response, errExecute := manager.Execute(ctx, providers, cliproxyexecutor.Request{Model: model}, opts)
				mu.Lock()
				tried := append([]string(nil), attempts[before:]...)
				mu.Unlock()
				if errExecute != nil || string(response.Payload) != wantID {
					t.Fatalf("%s: auth=%q error=%v, want %s; upstream attempts=%v", label, response.Payload, errExecute, wantID, tried)
				}
				if len(tried) != 1 {
					t.Fatalf("%s: upstream attempts=%v, want exactly one", label, tried)
				}
			}
			assertSelect := func(label string, opts cliproxyexecutor.Options, wantID string) {
				t.Helper()
				if mixed {
					// SelectAuth only covers single-provider selection.
					return
				}
				selected, errSelect := manager.SelectAuth(ctx, "claude", model, opts)
				if errSelect != nil || selected == nil || selected.ID != wantID {
					t.Fatalf("%s: SelectAuth() = %v, %v; want %s", label, selected, errSelect, wantID)
				}
			}

			// Nothing is observed yet (as after every restart): each credential gets one
			// probe request, in incoming (ID) order, and its answer is observed.
			probed := []string{idA, idB, idC}
			if mixed {
				probed = append(probed, idD)
			}
			for _, id := range probed {
				assertRoute("probe "+id, options("probe-"+id), id)
			}
			for _, id := range probed {
				observed, ok := manager.GetByID(id)
				if !ok || observed == nil || observed.Quota.ObservedAt.IsZero() || len(observed.Quota.Signals) == 0 {
					t.Fatalf("auth %s quota snapshot not observed: %#v", id, observed)
				}
			}
			if affinity {
				// A probe binds its session like any cold pick; the binding is kept.
				assertRoute("probe session keeps its credential", options("probe-"+idA), idA)
			}

			// b's weekly window resets first, so new work goes there.
			assertSelect("select picks the earliest reset", options("first"), idB)
			assertRoute("new session goes to the earliest reset", options("first"), idB)
			assertRoute("same session stays on the earliest reset", options("first"), idB)

			// b answers successfully but reports its weekly window used up. The pick that
			// gets this answer still goes to b; the answer is observed afterwards.
			mu.Lock()
			headers[idB] = soonestResetClaudeHeaders(now.Add(24*time.Hour), "rejected")
			mu.Unlock()
			assertRoute("b answers with a used-up weekly window", options("first"), idB)
			if observed, _ := manager.GetByID(idB); observed == nil ||
				!observedQuotaWindows(observed.Provider, observed.Quota.Signals, observed.Quota.ObservedAt, time.Now()).exhausted {
				t.Fatalf("auth %s snapshot does not report a used-up window: %#v", idB, observed)
			}
			if observed, _ := manager.GetByID(idB); observed == nil || observed.Quota.Exceeded || observed.Unavailable {
				t.Fatalf("auth %s was cooled down by a successful answer; ranking alone must move traffic: %#v", idB, observed)
			}

			// A new session moves to the next-earliest reset (c, 48h).
			assertSelect("select skips the exhausted credential", options("second"), idC)
			assertRoute("new session skips the exhausted credential", options("second"), idC)

			if affinity {
				// The bound session keeps its credential: b is still available (only a
				// cooldown unbinds a session) and session affinity does not consult the
				// fallback selector on a cache hit.
				assertRoute("bound session stays on its credential", options("first"), idB)

				// Once upstream refuses with a 429 the credential cools down and the
				// bound session fails over to the next-earliest reset.
				retryAfter := time.Hour
				manager.MarkResult(ctx, Result{
					AuthID: idB, Provider: "claude", Model: model, RetryAfter: &retryAfter,
					Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "weekly limit reached"},
				})
				assertRoute("bound session fails over after cooldown", options("first"), idC)
				assertRoute("rebound session stays on the new credential", options("first"), idC)
			} else {
				assertRoute("without affinity every request follows the ranking", options("first"), idC)
			}

			// c also runs out; with b exhausted too, the codex credential (72h) or a
			// (96h) is next, and the exhausted credentials are only a last resort.
			mu.Lock()
			headers[idC] = soonestResetClaudeHeaders(now.Add(48*time.Hour), "rejected")
			mu.Unlock()
			assertRoute("c answers with a used-up weekly window", options("second"), idC)
			next := idA
			if mixed {
				next = idD
			}
			assertRoute("next new session goes to the next usable reset", options("third"), next)
		})
	}
}

// TestManagerUseSchedulerFastPathBySelector guards that soonest-reset is a built-in
// selector (the manager prevalidates its candidates and restores cooldown error models)
// that never takes the scheduler fast path: the scheduler has no soonest-reset emulation
// and would fall back to round-robin without calling the selector.
func TestManagerUseSchedulerFastPathBySelector(t *testing.T) {
	tests := []struct {
		name         string
		selector     Selector
		wantBuiltIn  bool
		wantFastPath bool
	}{
		{name: "round-robin", selector: &RoundRobinSelector{}, wantBuiltIn: true, wantFastPath: true},
		{name: "weighted-round-robin", selector: &WeightedRoundRobinSelector{}, wantBuiltIn: true, wantFastPath: true},
		{name: "fill-first", selector: &FillFirstSelector{}, wantBuiltIn: true, wantFastPath: true},
		{name: "soonest-reset", selector: &SoonestResetSelector{}, wantBuiltIn: true, wantFastPath: false},
		{name: "session-affinity", selector: NewSessionAffinitySelector(&SoonestResetSelector{}), wantBuiltIn: false, wantFastPath: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if stoppable, ok := tt.selector.(StoppableSelector); ok {
				t.Cleanup(stoppable.Stop)
			}
			if got := isBuiltInSelector(tt.selector); got != tt.wantBuiltIn {
				t.Fatalf("isBuiltInSelector(%T) = %v, want %v", tt.selector, got, tt.wantBuiltIn)
			}
			manager := NewManager(nil, tt.selector, nil)
			if got := manager.useSchedulerFastPath(); got != tt.wantFastPath {
				t.Fatalf("useSchedulerFastPath() = %v, want %v", got, tt.wantFastPath)
			}
		})
	}
}

// TestManagerSoonestResetConcurrentBurstProbesOncePerCredential sends a burst of new
// requests to fresh credentials: each credential gets exactly one probe, and the rest of
// the burst goes by the normal order.
func TestManagerSoonestResetConcurrentBurstProbesOncePerCredential(t *testing.T) {
	ctx := context.Background()
	model := "claude-soonest-reset-burst"
	manager := NewManager(nil, &SoonestResetSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	var mu sync.Mutex
	counts := make(map[string]int)
	manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: "claude", executeFn: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		// No quota headers: every probe teaches nothing, so no credential becomes known.
		mu.Lock()
		counts[auth.ID]++
		mu.Unlock()
		return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
	}})
	ids := []string{"soonest-reset-burst-a", "soonest-reset-burst-b", "soonest-reset-burst-c"}
	for _, id := range ids {
		registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		if _, errRegister := manager.Register(ctx, &Auth{ID: id, Provider: "claude", Status: StatusActive}); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", id, errRegister)
		}
	}

	const requests = 50
	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, requests)
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, errExecute := manager.Execute(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errExecute != nil {
				errCh <- errExecute
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for errExecute := range errCh {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if counts[ids[0]] != requests-2 || counts[ids[1]] != 1 || counts[ids[2]] != 1 {
		t.Fatalf("upstream requests per credential = %v, want %s=%d and one probe each for the others", counts, ids[0], requests-2)
	}
}
