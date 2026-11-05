package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type inactiveProxyScheduler struct{}

func (*inactiveProxyScheduler) HasScheduler() bool { return false }
func (*inactiveProxyScheduler) PickAuth(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	return pluginapi.SchedulerPickResponse{}, false, nil
}

func TestProxyCooldownInitialAndCached(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, mode := range []string{"execute", "stream", "bootstrap"} {
		stream := mode != "execute"
		bootstrap := mode == "bootstrap"
		for _, affinity := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/plain", true: "/affinity"}[affinity], func(t *testing.T) {
				selector := Selector(&FillFirstSelector{})
				if affinity {
					selector = NewSessionAffinitySelector(selector)
				}
				m := NewManager(nil, selector, nil)
				m.SetRetryConfig(0, 0, 0)
				m.SetPluginScheduler(&inactiveProxyScheduler{})
				model := "proxy-cooldown-model"
				calls := 0
				execute := func(ctx context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					calls++
					cliproxyexecutor.MarkUpstreamAttempt(ctx)
					delay := time.Hour
					if a.Attributes["priority"] == "100" {
						delay = 5 * 24 * time.Hour
					}
					return cliproxyexecutor.Response{}, customStatusError{code: 429, msg: "synthetic original rejection", retryAfter: &delay}
				}
				m.RegisterExecutor(&customStreamMockExecutor{identifier: "claude", mockCustomErrorExecutor: mockCustomErrorExecutor{executeFn: execute}, streamFn: func(ctx context.Context, a *Auth, r cliproxyexecutor.Request, o cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					_, e := execute(ctx, a, r, o)
					if bootstrap {
						ch := make(chan cliproxyexecutor.StreamChunk, 1)
						ch <- cliproxyexecutor.StreamChunk{Err: e}
						close(ch)
						return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Synthetic-Bootstrap": []string{"retained"}}, Chunks: ch}, nil
					}
					return nil, e
				}})
				for _, p := range []string{"100", "0"} {
					id := t.Name() + p
					registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
					if _, err := m.Register(context.Background(), &Auth{ID: id, Provider: "claude", Status: StatusActive, Attributes: map[string]string{"priority": p}}); err != nil {
						t.Fatal(err)
					}
				}
				for round := 0; round < 2; round++ {
					var err error
					if stream {
						var result *cliproxyexecutor.StreamResult
						result, err = m.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
						if result != nil {
							chunk, ok := <-result.Chunks
							if !ok || chunk.Err == nil {
								t.Fatal("expected bootstrap error chunk")
							}
							err = chunk.Err
							if round == 0 && bootstrap && result.Headers.Get("X-Synthetic-Bootstrap") != "retained" {
								t.Fatal("bootstrap headers lost")
							}
						}
					} else {
						_, err = m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
					}
					var timed interface{ ProxyCooldownNextReevaluation() time.Time }
					if !errors.As(err, &timed) {
						t.Fatalf("round %d: no typed proxy deadline: %T %v", round, err, err)
					}
					low, _ := m.GetByID(t.Name() + "0")
					_, _, want := isAuthBlockedForModel(low, model, time.Now())
					if !timed.ProxyCooldownNextReevaluation().Equal(want) {
						t.Fatalf("deadline=%v want exact %v", timed.ProxyCooldownNextReevaluation(), want)
					}
					if statusCodeFromError(err) != http.StatusTooManyRequests {
						t.Fatalf("status=%d", statusCodeFromError(err))
					}
					if round == 0 && err.Error() != "synthetic original rejection" {
						t.Fatalf("lost original detail: %v", err)
					}
				}
				if calls != 2 {
					t.Fatalf("upstream calls=%d want two; cached request must not execute", calls)
				}
			})
		}
	}
}

func TestProxyCooldownSummaryFiltersAndUnknowns(t *testing.T) {
	now := time.Unix(2000000000, 123456789)
	model := "summary-model"
	makeAuth := func(id, priority string, delay time.Duration) *Auth {
		return &Auth{ID: id, Provider: "claude", Status: StatusActive, Attributes: map[string]string{"priority": priority}, ModelStates: map[string]*ModelState{model: {Unavailable: true, NextRetryAfter: now.Add(delay), Quota: QuotaState{Exceeded: true, NextRecoverAt: now.Add(delay)}}}}
	}
	for _, name := range []string{"all-known", "available", "unknown", "nonquota", "expired", "pinned", "other-model", "other-provider", "shared-before-model", "non429", "no-candidates", "required-kind", "credential-policy", "active-plugin", "custom-selector", "home"} {
		t.Run(name, func(t *testing.T) {
			m := NewManager(nil, &RoundRobinSelector{}, nil)
			m.RegisterExecutor(&customStreamMockExecutor{identifier: "claude"})
			primary := makeAuth("primary-"+t.Name(), "100", 5*24*time.Hour)
			backup := makeAuth("backup-"+t.Name(), "0", time.Hour)
			opts := cliproxyexecutor.Options{}
			ctx := context.Background()
			want := now.Add(time.Hour)
			valid := true
			cause := error(customStatusError{code: 429, msg: "original"})
			switch name {
			case "required-kind":
				ctx = withRequiredAuthKind(ctx, AuthKindOAuth)
				primary.Attributes[AttributeAuthKind] = AuthKindOAuth
				backup.Attributes[AttributeAuthKind] = AuthKindAPIKey
				backup.ModelStates = nil
				want = now.Add(5 * 24 * time.Hour)
			case "credential-policy":
				ctx = withCredentialPolicy(ctx, CredentialPolicyCodexAlphaSearchV1)
				valid = false
			case "active-plugin":
				m.SetPluginScheduler(&fakePluginScheduler{})
				valid = false
			case "custom-selector":
				m.selector = &proxyCooldownCustomSelector{}
				valid = false
			case "home":
				m.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
				valid = false
			case "available":
				backup.ModelStates = nil
				valid = false
			case "unknown":
				backup.ModelStates[model].NextRetryAfter = time.Time{}
				backup.ModelStates[model].Quota.NextRecoverAt = time.Time{}
				valid = false
			case "nonquota":
				backup.ModelStates[model].Quota.Exceeded = false
				valid = false
			case "expired":
				backup.ModelStates[model].NextRetryAfter = now.Add(-time.Second)
				backup.ModelStates[model].Quota.NextRecoverAt = now.Add(-time.Second)
				valid = false
			case "pinned":
				opts.Metadata = map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: primary.ID}
				want = now.Add(5 * 24 * time.Hour)
			case "shared-before-model":
				backup.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(time.Minute)}
				want = now.Add(time.Minute)
			case "non429":
				cause = customStatusError{code: 503, msg: "service unavailable"}
				valid = false
			case "no-candidates":
				primary.Disabled = true
				backup.Disabled = true
				valid = false
			}
			m.auths[primary.ID] = primary
			m.auths[backup.ID] = backup
			for _, a := range []*Auth{primary, backup} {
				models := []*registry.ModelInfo{{ID: model}}
				if name == "other-model" && a == backup {
					models = []*registry.ModelInfo{{ID: "unrelated"}}
					want = now.Add(5 * 24 * time.Hour)
				}
				if name == "other-provider" && a == backup {
					a.Provider = "codex"
					want = now.Add(5 * 24 * time.Hour)
				}
				registry.GetGlobalRegistry().RegisterClient(a.ID, a.Provider, models)
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
			}
			got := m.withProxyCooldownSummary(ctx, []string{"claude"}, model, opts, cause, now)
			deadline, ok := ProxyCooldownNextReevaluation(got)
			if ok != valid {
				t.Fatalf("valid=%t want %t: %T %v", ok, valid, got, got)
			}
			if valid && !deadline.Equal(want) {
				t.Fatalf("deadline=%v want %v", deadline, want)
			}
			if !errors.Is(got, cause) {
				t.Fatal("lost original cause")
			}
			if selector, ok := m.selector.(*RoundRobinSelector); ok && len(selector.lastPicked) != 0 {
				t.Fatal("read-only summary mutated selector")
			}
		})
	}
}

func TestProxyCooldownWeightedEligibility(t *testing.T) {
	now := time.Unix(2000000000, 0)
	for _, affinity := range []bool{false, true} {
		for _, state := range []string{"available", "unknown", "earlier", "positive-control"} {
			t.Run(map[bool]string{false: "plain/", true: "affinity/"}[affinity]+state, func(t *testing.T) {
				selector := Selector(&WeightedRoundRobinSelector{})
				if affinity {
					selector = NewSessionAffinitySelector(selector)
				}
				m := NewManager(nil, selector, nil)
				m.RegisterExecutor(&customStreamMockExecutor{identifier: "claude"})
				primary := &Auth{ID: t.Name() + "primary", Provider: "claude", Attributes: map[string]string{AttributeWeight: "1"}, Unavailable: true, Quota: QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(5 * time.Hour)}}
				backup := &Auth{ID: t.Name() + "backup", Provider: "claude", Attributes: map[string]string{AttributeWeight: "0"}}
				switch state {
				case "unknown":
					backup.Unavailable = true
					backup.Quota = QuotaState{Exceeded: true}
				case "earlier", "positive-control":
					backup.Unavailable = true
					backup.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(time.Hour)}
				}
				want := now.Add(5 * time.Hour)
				if state == "positive-control" {
					backup.Attributes[AttributeWeight] = "1"
					want = now.Add(time.Hour)
				}
				for _, a := range []*Auth{primary, backup} {
					m.auths[a.ID] = a
					registry.GetGlobalRegistry().RegisterClient(a.ID, "claude", []*registry.ModelInfo{{ID: "weighted-model"}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
				}
				err := m.withProxyCooldownSummary(context.Background(), []string{"claude"}, "weighted-model", cliproxyexecutor.Options{}, customStatusError{code: 429, msg: "original"}, now)
				got, ok := ProxyCooldownNextReevaluation(err)
				if !ok || !got.Equal(want) {
					t.Fatalf("deadline=%v known=%t want %v", got, ok, want)
				}
			})
		}
	}
}

// An unknown scheduler's availability rules cannot be inferred locally.
type proxyCooldownCustomSelector struct{ Selector }
