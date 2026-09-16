package auth

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// TestSessionAffinitySchedulerSelectionMatchesLegacy drives two identically configured managers
// through the same randomized sequence of picks and results. One manager selects through the
// legacy scan, the other through the scheduler-backed path; every step must choose the same
// credential or fail with the same status.
func TestSessionAffinitySchedulerSelectionMatchesLegacy(t *testing.T) {
	previousLevel := log.GetLevel()
	log.SetLevel(log.ErrorLevel)
	t.Cleanup(func() { log.SetLevel(previousLevel) })
	withQuotaCooldownEnabled(t)

	fallbacks := []struct {
		name     string
		selector func() Selector
		weighted bool
	}{
		{name: "round-robin", selector: func() Selector { return &RoundRobinSelector{} }},
		{name: "weighted-round-robin", selector: func() Selector { return &WeightedRoundRobinSelector{} }, weighted: true},
		{name: "fill-first", selector: func() Selector { return &FillFirstSelector{} }},
	}
	modes := []struct {
		name      string
		providers int
		mixed     bool
	}{
		{name: "single", providers: 1},
		{name: "mixed-single-provider", providers: 1, mixed: true},
		{name: "mixed-two-providers", providers: 2, mixed: true},
	}

	for _, fallback := range fallbacks {
		for _, mode := range modes {
			t.Run(fallback.name+"/"+mode.name, func(t *testing.T) {
				const credentials = 60
				const steps = 600
				const model = "affinity-parity-model"
				rng := rand.New(rand.NewSource(20260916))
				providers := make([]string, 0, mode.providers)
				for index := range mode.providers {
					providers = append(providers, fmt.Sprintf("affinity-parity-%s-%d", fallback.name, index))
				}
				newManager := func() *Manager {
					selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: fallback.selector(), TTL: time.Hour})
					t.Cleanup(selector.Stop)
					manager := NewManager(nil, selector, nil)
					for _, provider := range providers {
						manager.RegisterExecutor(schedulerBenchmarkExecutor{id: provider})
					}
					return manager
				}
				legacy := newManager()
				scheduled := newManager()

				ctx := WithSkipPersist(context.Background())
				now := time.Now()
				reg := registry.GetGlobalRegistry()
				ids := make([]string, 0, credentials)
				for index := range credentials {
					provider := providers[index%len(providers)]
					auth := &Auth{
						ID:         fmt.Sprintf("affinity-parity-%s-%s-%03d", fallback.name, mode.name, index),
						Provider:   provider,
						Status:     StatusActive,
						Attributes: map[string]string{"priority": strconv.Itoa(rng.Intn(3))},
						Metadata:   map[string]any{},
					}
					auth.Metadata["access_token"] = benchmarkAccessToken(auth.ID, now.Add(time.Hour))
					if fallback.weighted {
						auth.Attributes[AttributeWeight] = strconv.Itoa(rng.Intn(4))
					}
					switch rng.Intn(9) {
					case 0:
						auth.Disabled = true
					case 1:
						auth.Status = StatusDisabled
					case 2:
						auth.Unavailable = true
						auth.Status = StatusError
						auth.LastError = &Error{Code: "unauthorized", Message: "unauthorized", HTTPStatus: http.StatusUnauthorized}
					case 3:
						auth.ModelStates = map[string]*ModelState{model: {
							Unavailable:    true,
							NextRetryAfter: now.Add(time.Hour),
							Quota:          QuotaState{Exceeded: true, NextRecoverAt: now.Add(time.Hour)},
						}}
					case 4:
						auth.ModelStates = map[string]*ModelState{model: {Unavailable: true, NextRetryAfter: now.Add(-time.Hour)}}
					case 5:
						auth.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(time.Hour)}
					case 6:
						auth.Metadata["access_token"] = benchmarkAccessToken(auth.ID, now.Add(-time.Hour))
					}
					reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
					ids = append(ids, auth.ID)
					for _, manager := range []*Manager{legacy, scheduled} {
						if _, errRegister := manager.Register(ctx, auth.Clone()); errRegister != nil {
							t.Fatalf("register %s: %v", auth.ID, errRegister)
						}
					}
				}
				t.Cleanup(func() {
					for _, id := range ids {
						reg.UnregisterClient(id)
					}
				})

				sessionOptions := func(session string) cliproxyexecutor.Options {
					opts := cliproxyexecutor.Options{Metadata: map[string]any{}}
					if session != "" {
						opts.Headers = http.Header{"X-Session-ID": []string{session}}
					}
					return opts
				}
				pickLegacy := func(opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, error) {
					if mode.mixed {
						auth, _, _, errPick := legacy.pickNextMixedLegacy(context.Background(), providers, model, opts, tried)
						return auth, errPick
					}
					auth, _, errPick := legacy.pickNextLegacy(context.Background(), providers[0], model, opts, tried)
					return auth, errPick
				}
				pickScheduled := func(opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, error) {
					if mode.mixed {
						auth, _, _, errPick := scheduled.pickNextMixed(context.Background(), providers, model, opts, tried)
						return auth, errPick
					}
					auth, _, errPick := scheduled.pickNext(context.Background(), providers[0], model, opts, tried)
					return auth, errPick
				}

				sessions := []string{"session-a", "session-b", "session-c", "session-d", "session-e", ""}
				servedByScheduler := 0
				for step := range steps {
					session := sessions[rng.Intn(len(sessions))]
					var tried map[string]struct{}
					if rng.Intn(4) == 0 {
						tried = map[string]struct{}{ids[rng.Intn(credentials)]: {}}
					}
					want, errWant := pickLegacy(sessionOptions(session), tried)
					got, errGot := pickScheduled(sessionOptions(session), tried)
					if (errWant == nil) != (errGot == nil) {
						t.Fatalf("step %d session %q: legacy err = %v, scheduler err = %v", step, session, errWant, errGot)
					}
					if errWant != nil {
						if statusCodeFromError(errWant) != statusCodeFromError(errGot) {
							t.Fatalf("step %d session %q: legacy err = %v, scheduler err = %v", step, session, errWant, errGot)
						}
						continue
					}
					if want.ID != got.ID {
						t.Fatalf("step %d session %q tried %v: legacy picked %s, scheduler picked %s", step, session, tried, want.ID, got.ID)
					}
					if len(scheduled.scheduler.readyAuths(context.Background(), providers, model, sessionOptions(session), tried)) > 0 {
						servedByScheduler++
					}
					if rng.Intn(3) != 0 {
						continue
					}
					result := Result{AuthID: want.ID, Provider: want.Provider, Model: model, Success: true}
					if rng.Intn(6) == 0 {
						retryAfter := time.Hour
						result = Result{
							AuthID:     want.ID,
							Provider:   want.Provider,
							Model:      model,
							RetryAfter: &retryAfter,
							Error:      &Error{Code: "rate_limited", Message: "quota", HTTPStatus: http.StatusTooManyRequests},
						}
					}
					legacy.MarkResult(ctx, result)
					scheduled.MarkResult(ctx, result)
				}
				if servedByScheduler == 0 {
					t.Fatal("no pick was served from the scheduler ready view")
				}
			})
		}
	}
}

func TestMergeAuthsByID(t *testing.T) {
	auths := func(ids ...string) []*Auth {
		out := make([]*Auth, 0, len(ids))
		for _, id := range ids {
			out = append(out, &Auth{ID: id})
		}
		return out
	}
	idsOf := func(list []*Auth) []string {
		out := make([]string, 0, len(list))
		for _, auth := range list {
			out = append(out, auth.ID)
		}
		return out
	}

	if merged := mergeAuthsByID(nil); merged != nil {
		t.Fatalf("mergeAuthsByID(nil) = %v, want nil", idsOf(merged))
	}
	if got, want := idsOf(mergeAuthsByID([][]*Auth{auths("x", "y")})), []string{"x", "y"}; !slices.Equal(got, want) {
		t.Fatalf("single group = %v, want %v", got, want)
	}
	if got, want := idsOf(mergeAuthsByID([][]*Auth{auths("b", "d", "f"), auths("a", "e"), auths("c")})), []string{"a", "b", "c", "d", "e", "f"}; !slices.Equal(got, want) {
		t.Fatalf("merged groups = %v, want %v", got, want)
	}
}
