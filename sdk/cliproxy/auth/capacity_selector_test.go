package auth

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestObservedCapacity(t *testing.T) {
	now := time.Unix(1800000000, 0)
	for _, tc := range []struct {
		name, provider, model string
		signals               map[string]string
		age                   time.Duration
		want                  float64
		known                 bool
	}{
		{"Claude tightest window", "claude", "claude-sonnet-4-6", map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.1", "Anthropic-Ratelimit-Unified-7d-Utilization": "0.8"}, 0, 0.2, true},
		{"Claude model window", "claude", "claude-opus-5", map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.1", "Anthropic-Ratelimit-Unified-7d-opus-Utilization": "0.95", "Anthropic-Ratelimit-Unified-7d-sonnet-Utilization": "1"}, 0, 0.05, true},
		{"Codex tightest window", "codex", "gpt-6-sol", map[string]string{"X-Codex-Primary-Used-Percent": "30", "X-Codex-Secondary-Used-Percent": "60"}, 0, 0.4, true},
		{"Codex limit reached", "codex", "model", map[string]string{"X-Codex-Primary-Used-Percent": "2", "X-Codex-Limit-Reached": "true"}, 0, 0, true},
		{"Codex disallowed", "codex", "model", map[string]string{"X-Codex-Primary-Used-Percent": "2", "X-Codex-Allowed": "False"}, 0, 0, true},
		{"Codex named model disallowed", "codex", "gpt-5.3-codex-spark", map[string]string{"X-Codex-Primary-Used-Percent": "2", "X-Codex-Bengalfox-Limit-Name": "GPT-5.3-Codex-Spark", "X-Codex-Bengalfox-Allowed": "false"}, 0, 0, true},
		{"Codex unrelated model", "codex", "gpt-6-sol", map[string]string{"X-Codex-Primary-Used-Percent": "20", "X-Codex-Additional-Spark-Limit-Name": "GPT-5.3-Codex-Spark", "X-Codex-Additional-Spark-Primary-Used-Percent": "99"}, 0, 0.8, true},
		{"Codex named model", "codex", "gpt-5.3-codex-spark", map[string]string{"X-Codex-Primary-Used-Percent": "20", "X-Codex-Bengalfox-Limit-Name": "GPT-5.3-Codex-Spark", "X-Codex-Bengalfox-Primary-Used-Percent": "99"}, 0, 0.01, true},
		{"old observation", "codex", "model", map[string]string{"X-Codex-Primary-Used-Percent": "0"}, capacityObservationMaxAge, 0, false},
		{"future observation", "codex", "model", map[string]string{"X-Codex-Primary-Used-Percent": "0"}, -time.Second, 0, false},
		{"expired absolute reset", "codex", "model", map[string]string{"X-Codex-Primary-Used-Percent": "0", "X-Codex-Primary-Reset-At": strconv.FormatInt(now.Unix(), 10)}, 0, 0, false},
		{"expired relative reset", "codex", "model", map[string]string{"X-Codex-Primary-Used-Percent": "0", "X-Codex-Primary-Reset-After-Seconds": "5"}, 5 * time.Second, 0, false},
		{"valid relative reset", "codex", "model", map[string]string{"X-Codex-Primary-Used-Percent": "20", "X-Codex-Primary-Reset-After-Seconds": "60"}, time.Second, 0.8, true},
		{"malformed sibling", "claude", "model", map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.1", "Anthropic-Ratelimit-Unified-7d-Utilization": "bad"}, 0, 0, false},
		{"NaN", "codex", "model", map[string]string{"X-Codex-Primary-Used-Percent": "NaN"}, 0, 0, false},
		{"over limit", "codex", "model", map[string]string{"X-Codex-Primary-Used-Percent": "101"}, 0, 0, false},
		{"unsupported provider", "gemini", "model", map[string]string{"X-Codex-Primary-Used-Percent": "0"}, 0, 0, false},
		{"no signals", "codex", "model", nil, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := &Auth{Provider: tc.provider, Quota: QuotaState{ObservedAt: now.Add(-tc.age), Signals: tc.signals}}
			got, known := observedCapacity(auth, tc.model, now)
			if known != tc.known || math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("capacity = (%v, %v), want (%v, %v)", got, known, tc.want, tc.known)
			}
		})
	}
}

func capacityTestAuth(id string, used float64, now time.Time) *Auth {
	return &Auth{ID: id, Provider: "codex", Status: StatusActive, Quota: QuotaState{
		ObservedAt: now,
		Signals:    map[string]string{"X-Codex-Primary-Used-Percent": strconv.FormatFloat(used, 'f', -1, 64)},
	}}
}

func TestCapacitySelectorSelection(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name string
		make func() []*Auth
		want []string
	}{
		{"most remaining", func() []*Auth { return []*Auth{capacityTestAuth("a", 95, now), capacityTestAuth("b", 20, now)} }, []string{"b", "b"}},
		{"ties rotate", func() []*Auth { return []*Auth{capacityTestAuth("b", 20, now), capacityTestAuth("a", 20, now)} }, []string{"a", "b", "a"}},
		{"unknown is not full", func() []*Auth { return []*Auth{{ID: "a", Provider: "codex"}, capacityTestAuth("b", 20, now)} }, []string{"b"}},
		{"unknown before low capacity", func() []*Auth { return []*Auth{{ID: "a", Provider: "codex"}, capacityTestAuth("b", 99, now)} }, []string{"a"}},
		{"unknown before exhausted", func() []*Auth { return []*Auth{capacityTestAuth("a", 100, now), {ID: "b", Provider: "codex"}} }, []string{"b"}},
		{"all unknown rotate", func() []*Auth { return []*Auth{{ID: "a"}, {ID: "b"}} }, []string{"a", "b"}},
		{"stale is explored before low capacity", func() []*Auth {
			return []*Auth{capacityTestAuth("a", 0, now.Add(-time.Hour)), capacityTestAuth("b", 90, now)}
		}, []string{"a"}},
		{"disabled excluded", func() []*Auth {
			a := capacityTestAuth("a", 0, now)
			a.Disabled = true
			return []*Auth{a, capacityTestAuth("b", 90, now)}
		}, []string{"b"}},
		{"priority wins", func() []*Auth {
			a := capacityTestAuth("a", 90, now)
			a.Attributes = map[string]string{"priority": "2"}
			return []*Auth{a, capacityTestAuth("b", 0, now)}
		}, []string{"a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selector := &CapacitySelector{nowFunc: func() time.Time { return now }}
			auths := tc.make()
			for _, want := range tc.want {
				got, errPick := selector.Pick(context.Background(), "codex", "model", cliproxyexecutor.Options{}, auths)
				if errPick != nil || got == nil || got.ID != want {
					t.Fatalf("pick = %v, %v; want %s", got, errPick, want)
				}
			}
		})
	}
}

func TestCapacitySelectorPreservesSessionAndFailsOver(t *testing.T) {
	now := time.Now()
	selector := NewSessionAffinitySelector(&CapacitySelector{})
	t.Cleanup(selector.Stop)
	auths := []*Auth{capacityTestAuth("a", 95, now), capacityTestAuth("b", 20, now)}
	baseline, errBaseline := (&RoundRobinSelector{}).Pick(context.Background(), "codex", "model", cliproxyexecutor.Options{}, auths)
	if errBaseline != nil || baseline.ID != "a" {
		t.Fatalf("round-robin baseline = %v, %v; want depleted account a", baseline, errBaseline)
	}
	opts := cliproxyexecutor.Options{Headers: http.Header{"Session-Id": []string{"stable"}}}
	pick := func(options cliproxyexecutor.Options, want string) {
		t.Helper()
		got, errPick := selector.Pick(context.Background(), "codex", "model", options, auths)
		if errPick != nil || got == nil || got.ID != want {
			t.Fatalf("pick = %v, %v; want %s", got, errPick, want)
		}
	}
	pick(opts, "b")
	auths[0].Quota = capacityTestAuth("a", 0, now).Quota
	auths[1].Quota = capacityTestAuth("b", 99, now).Quota
	pick(opts, "b")
	pick(cliproxyexecutor.Options{Headers: http.Header{"Session-Id": []string{"new"}}}, "a")
	auths[1].Disabled = true
	pick(opts, "a")
}

func TestCapacitySelectorExploresUnknownAccounts(t *testing.T) {
	now := time.Now()
	selector := &CapacitySelector{nowFunc: func() time.Time { return now }}
	auths := []*Auth{capacityTestAuth("known", 50, now), {ID: "unknown-a", Provider: "codex"}, {ID: "unknown-b", Provider: "codex"}}
	for index := 1; index <= 2*capacityExploreEvery; index++ {
		got, errPick := selector.Pick(context.Background(), "codex", "model", cliproxyexecutor.Options{}, auths)
		want := "known"
		if index == capacityExploreEvery {
			want = "unknown-a"
		} else if index == 2*capacityExploreEvery {
			want = "unknown-b"
		}
		if errPick != nil || got == nil || got.ID != want {
			t.Fatalf("assignment %d = %v, %v; want %s", index, got, errPick, want)
		}
	}
}

func TestCapacitySelectorPrefersWebsocketEligibleAccounts(t *testing.T) {
	now := time.Now()
	auths := []*Auth{capacityTestAuth("a", 0, now), capacityTestAuth("b", 80, now)}
	auths[1].Attributes = map[string]string{"websockets": "true"}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	got, errPick := (&CapacitySelector{}).Pick(ctx, "codex", "model", cliproxyexecutor.Options{}, auths)
	if errPick != nil || got == nil || got.ID != "b" {
		t.Fatalf("websocket selection = %v, %v; want b", got, errPick)
	}
}

func TestManagerCapacitySelectorResolvesModelAliases(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, affinity := range []bool{false, true} {
			t.Run(provider+"/"+strconv.FormatBool(affinity), func(t *testing.T) {
				ctx := context.Background()
				route := "friendly-" + t.Name()
				upstream := "claude-opus-5"
				if provider == "codex" {
					upstream = "gpt-5.3-codex-spark"
				}
				var selector Selector = &CapacitySelector{}
				if affinity {
					s := NewSessionAffinitySelector(selector)
					t.Cleanup(s.Stop)
					selector = s
				}
				manager := NewManager(nil, selector, nil)
				manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: provider})
				manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{provider: {{Name: upstream, Alias: route, Fork: true}}})
				for index, id := range []string{"a", "b"} {
					auth := &Auth{ID: t.Name() + id, Provider: provider, Quota: QuotaState{ObservedAt: time.Now(), Signals: map[string]string{}}}
					if provider == "claude" {
						auth.Quota.Signals["Anthropic-Ratelimit-Unified-5h-Utilization"] = []string{"0.1", "0.4"}[index]
						auth.Quota.Signals["Anthropic-Ratelimit-Unified-7d-opus-Utilization"] = []string{"0.99", "0.5"}[index]
					} else {
						auth.Quota.Signals["X-Codex-Primary-Used-Percent"] = []string{"10", "40"}[index]
						auth.Quota.Signals["X-Codex-Bengalfox-Limit-Name"] = "GPT-5.3-Codex-Spark"
						auth.Quota.Signals["X-Codex-Bengalfox-Primary-Used-Percent"] = []string{"99", "50"}[index]
					}
					registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: upstream}, {ID: route}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
					if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
						t.Fatal(errRegister)
					}
				}
				picked, errPick := manager.SelectAuth(ctx, provider, route, cliproxyexecutor.Options{Headers: http.Header{"Session-Id": []string{"alias-session"}}})
				if errPick != nil || picked == nil || picked.ID != t.Name()+"b" {
					t.Fatalf("alias selection = %v, %v; want b", picked, errPick)
				}
			})
		}
	}
}

func TestManagerCapacitySelectorUsesLiveObservations(t *testing.T) {
	for _, affinity := range []bool{false, true} {
		for _, mixed := range []bool{false, true} {
			t.Run(strconv.FormatBool(affinity)+"/"+strconv.FormatBool(mixed), func(t *testing.T) {
				ctx := context.Background()
				model := "capacity-" + t.Name()
				var selector Selector = &CapacitySelector{}
				if affinity {
					s := NewSessionAffinitySelector(selector)
					t.Cleanup(s.Stop)
					selector = s
				}
				manager := NewManager(nil, selector, nil)
				manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: "codex"})
				for _, auth := range []*Auth{capacityTestAuth(t.Name()+"-a", 95, time.Now()), capacityTestAuth(t.Name()+"-b", 20, time.Now())} {
					registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
					if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
						t.Fatal(errRegister)
					}
				}
				opts := cliproxyexecutor.Options{Headers: http.Header{"Session-Id": []string{"stable"}}}
				var picked *Auth
				var errPick error
				pick := func(options cliproxyexecutor.Options) {
					if mixed {
						picked, _, _, errPick = manager.pickNextMixed(ctx, []string{"codex"}, model, options, nil)
					} else {
						picked, _, errPick = manager.pickNext(ctx, "codex", model, options, nil)
					}
				}
				pick(opts)
				if errPick != nil || picked == nil || picked.ID != t.Name()+"-b" {
					t.Fatalf("manager pick = %v, %v; want highest capacity", picked, errPick)
				}
				for id, used := range map[string]string{t.Name() + "-a": "0", t.Name() + "-b": "99"} {
					observationCtx := internallogging.WithResponseHeadersHolder(ctx)
					internallogging.SetResponseHeaders(observationCtx, http.Header{"X-Codex-Primary-Used-Percent": []string{used}})
					manager.MarkResult(observationCtx, Result{AuthID: id, Provider: "codex", Model: model, Success: true})
				}
				pick(opts)
				want := t.Name() + "-a"
				if affinity {
					want = t.Name() + "-b"
				}
				if errPick != nil || picked == nil || picked.ID != want {
					t.Fatalf("after observation update = %v, %v; want %s", picked, errPick, want)
				}
				pick(cliproxyexecutor.Options{Headers: http.Header{"Session-Id": []string{"new-session"}}})
				if errPick != nil || picked == nil || picked.ID != t.Name()+"-a" {
					t.Fatalf("new session after update = %v, %v; want account a", picked, errPick)
				}
			})
		}
	}
}
