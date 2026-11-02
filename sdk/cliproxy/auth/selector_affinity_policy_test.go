package auth

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func priorityPolicySelector(precedence bool) *SessionAffinitySelector {
	return NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &FillFirstSelector{}, TTL: time.Hour, RebindOn: SessionAffinityRebindOnQuota,
		ProviderOverrides: map[string]SessionAffinityProviderOverride{
			"claude": {Fallback: &RoundRobinSelector{}, PriorityPrecedence: precedence},
		},
	})
}

func priorityPolicyAuth(id, provider string, priority int) *Auth {
	return &Auth{ID: id, Provider: provider, Attributes: map[string]string{"priority": fmt.Sprint(priority)}}
}

func priorityPolicyOptions(session string) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{
		Headers:  http.Header{"X-Claude-Code-Session-Id": []string{session}},
		Metadata: map[string]any{},
	}
}

func pickPriorityPolicy(t *testing.T, selector *SessionAffinitySelector, provider string, opts cliproxyexecutor.Options, auths []*Auth) string {
	t.Helper()
	selected, err := selector.Pick(context.Background(), provider, "model", opts, auths)
	if err != nil || selected == nil {
		t.Fatalf("Pick() = %v, %v", selected, err)
	}
	return selected.ID
}

func TestProviderAffinityPolicyDistributesNewSessionsWithinHighestTier(t *testing.T) {
	selector := priorityPolicySelector(true)
	defer selector.Stop()
	auths := []*Auth{
		priorityPolicyAuth("a", "claude", 4), priorityPolicyAuth("b", "claude", 4),
		priorityPolicyAuth("lower", "claude", 3),
	}
	counts := map[string]int{}
	for i := 0; i < 12; i++ {
		opts := priorityPolicyOptions(fmt.Sprintf("session-%d", i))
		first := pickPriorityPolicy(t, selector, "claude", opts, auths)
		counts[first]++
		if second := pickPriorityPolicy(t, selector, "claude", opts, auths); second != first {
			t.Fatalf("same-tier affinity changed from %s to %s", first, second)
		}
	}
	if counts["a"] != 6 || counts["b"] != 6 || counts["lower"] != 0 {
		t.Fatalf("unexpected conversation distribution: %v", counts)
	}
}

func TestProviderAffinityPolicyPreemptsOnlyWhenOptedIn(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(fmt.Sprint(strict), func(t *testing.T) {
			selector := priorityPolicySelector(strict)
			defer selector.Stop()
			lower := priorityPolicyAuth("lower", "claude", 2)
			higher := priorityPolicyAuth("higher", "claude", 4)
			opts := priorityPolicyOptions("ongoing-conversation")
			if got := pickPriorityPolicy(t, selector, "claude", opts, []*Auth{lower}); got != "lower" {
				t.Fatalf("initial = %s", got)
			}
			want := "lower"
			if strict {
				want = "higher"
			}
			if got := pickPriorityPolicy(t, selector, "claude", opts, []*Auth{lower, higher}); got != want {
				t.Fatalf("after recovery = %s, want %s", got, want)
			}
		})
	}
}

func TestProviderAffinityPolicyKeepsOtherProvidersUnchanged(t *testing.T) {
	selector := priorityPolicySelector(true)
	defer selector.Stop()
	lower := priorityPolicyAuth("lower", "gemini", 2)
	higher := priorityPolicyAuth("higher", "gemini", 4)
	opts := priorityPolicyOptions("other-provider")
	pickPriorityPolicy(t, selector, "gemini", opts, []*Auth{lower})
	if got := pickPriorityPolicy(t, selector, "gemini", opts, []*Auth{lower, higher}); got != "lower" {
		t.Fatalf("other provider affinity changed: %s", got)
	}
	for i := 0; i < 3; i++ {
		if got := pickPriorityPolicy(t, selector, "gemini", priorityPolicyOptions(fmt.Sprint(i)), []*Auth{higher, priorityPolicyAuth("z", "gemini", 4)}); got != "higher" {
			t.Fatalf("global fill-first changed: %s", got)
		}
	}
}

func TestProviderAffinityPolicyDoesNotPreserveDemotedRetryBinding(t *testing.T) {
	for _, previousPriority := range []int{3, 4} {
		t.Run(fmt.Sprint(previousPriority), func(t *testing.T) {
			selector := priorityPolicySelector(true)
			defer selector.Stop()
			previous := priorityPolicyAuth("previous", "claude", previousPriority)
			current := priorityPolicyAuth("current", "claude", 4)
			opts := priorityPolicyOptions("retry-session")
			pickPriorityPolicy(t, selector, "claude", opts, []*Auth{previous})
			ctx := context.WithValue(context.Background(), prevalidatedAuthCandidatesKey{}, true)
			ctx = withSessionAffinityTransientAuthIDs(ctx, map[string]struct{}{"previous": {}})
			ctx = withSessionAffinityAuthPriorities(ctx, []*Auth{previous, current})
			picked, err := selector.Pick(ctx, "claude", "model", opts, []*Auth{current})
			if err != nil || picked == nil || picked.ID != "current" {
				t.Fatalf("retry = %v, %v", picked, err)
			}
			want := "current"
			if previousPriority == 4 {
				want = "previous"
			}
			key := "claude::" + opts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string) + "::model"
			if bound, _ := selector.cache.Get(key); bound != want {
				t.Fatalf("retained binding = %s, want %s", bound, want)
			}
		})
	}
}

func TestProviderAffinityPolicyParentAndGuardianRespectTopTier(t *testing.T) {
	for _, guardian := range []bool{false, true} {
		t.Run(fmt.Sprint(guardian), func(t *testing.T) {
			selector := priorityPolicySelector(true)
			defer selector.Stop()
			lower := priorityPolicyAuth("lower", "claude", 2)
			higher := priorityPolicyAuth("higher", "claude", 4)
			parent := priorityPolicyOptions("parent")
			pickPriorityPolicy(t, selector, "claude", parent, []*Auth{lower})
			child := priorityPolicyOptions("parent")
			child.Headers.Set("X-Claude-Code-Agent-Id", "child")
			if guardian {
				child.Headers.Set("X-Openai-Subagent", "guardian")
			}
			if got := pickPriorityPolicy(t, selector, "claude", child, []*Auth{lower, higher}); got != "higher" {
				t.Fatalf("child inherited a lower tier: %s", got)
			}
		})
	}
}

func TestProviderAffinityPolicyLCPRebindsAfterHigherTierRecovery(t *testing.T) {
	selector := priorityPolicySelector(true)
	defer selector.Stop()
	lower := priorityPolicyAuth("lower", "claude", 2)
	higher := priorityPolicyAuth("higher", "claude", 4)
	opts := func(body string) cliproxyexecutor.Options {
		return cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: []byte(body),
			Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "synthetic-caller"}}
	}
	first := opts(`{"messages":[{"role":"system","content":"stable"},{"role":"user","content":"first"}]}`)
	pickPriorityPolicy(t, selector, "claude", first, []*Auth{lower})
	grown := opts(`{"messages":[{"role":"system","content":"stable"},{"role":"user","content":"first"},{"role":"assistant","content":"answer"},{"role":"user","content":"continue"}]}`)
	if got := pickPriorityPolicy(t, selector, "claude", grown, []*Auth{lower, higher}); got != "higher" {
		t.Fatalf("LCP retained lower tier: %s", got)
	}
	if got := pickPriorityPolicy(t, selector, "claude", grown, []*Auth{lower, higher}); got != "higher" {
		t.Fatalf("LCP rebind did not persist: %s", got)
	}
}

func TestProviderAffinityPolicyHonorsModelCooldown(t *testing.T) {
	selector := priorityPolicySelector(true)
	defer selector.Stop()
	higher := priorityPolicyAuth("higher", "claude", 4)
	lower := priorityPolicyAuth("lower", "claude", 2)
	higher.ModelStates = map[string]*ModelState{"model": {Unavailable: true, NextRetryAfter: time.Now().Add(time.Hour),
		Quota: QuotaState{Exceeded: true, NextRecoverAt: time.Now().Add(time.Hour)}}}
	opts := priorityPolicyOptions("model-cooldown")
	if got := pickPriorityPolicy(t, selector, "claude", opts, []*Auth{higher, lower}); got != "lower" {
		t.Fatalf("cooled model selected: %s", got)
	}
	if got := pickPriorityPolicy(t, selector, "claude", opts, []*Auth{higher, lower}); got != "lower" {
		t.Fatalf("cooled model preempted lower tier: %s", got)
	}
	higher.ModelStates = nil
	if got := pickPriorityPolicy(t, selector, "claude", opts, []*Auth{higher, lower}); got != "higher" {
		t.Fatalf("recovered model did not preempt: %s", got)
	}
}

type priorityPolicyExecutor struct{ quotaAttemptIsolationExecutor }

func (*priorityPolicyExecutor) Identifier() string { return "claude" }

func TestProviderAffinityPolicyProductionMixedEntrypoint(t *testing.T) {
	selector := priorityPolicySelector(true)
	defer selector.Stop()
	manager := NewManager(nil, selector, nil)
	manager.RegisterExecutor(&priorityPolicyExecutor{})
	lower := priorityPolicyAuth("lower", "claude", 2)
	if _, err := manager.Register(context.Background(), lower); err != nil {
		t.Fatal(err)
	}
	opts := priorityPolicyOptions("production-session")
	first, _, _, err := manager.pickNextMixed(context.Background(), []string{"claude"}, "", opts, nil)
	if err != nil || first == nil || first.ID != "lower" {
		t.Fatalf("initial = %v, %v", first, err)
	}
	for _, id := range []string{"a", "b"} {
		if _, err = manager.Register(context.Background(), priorityPolicyAuth(id, "claude", 4)); err != nil {
			t.Fatal(err)
		}
	}
	recovered, _, _, err := manager.pickNextMixed(context.Background(), []string{"claude"}, "", opts, nil)
	if err != nil || recovered == nil || recovered.ID == "lower" {
		t.Fatalf("mixed path ignored priority: %v, %v", recovered, err)
	}
	cold, _, _, err := manager.pickNextMixed(context.Background(), []string{"claude"}, "", priorityPolicyOptions("new-session"), nil)
	if err != nil || cold == nil || cold.ID == recovered.ID || cold.ID == "lower" {
		t.Fatalf("mixed path ignored RR: %v, %v", cold, err)
	}
	// Explicit pins remain authority, even when they intentionally select a lower tier.
	pinned := priorityPolicyOptions("pinned-session")
	pinned.Metadata[cliproxyexecutor.PinnedAuthMetadataKey] = "lower"
	selected, _, _, err := manager.pickNextMixed(context.Background(), []string{"claude"}, "", pinned, nil)
	if err != nil || selected == nil || selected.ID != "lower" {
		t.Fatalf("explicit pin ignored: %v, %v", selected, err)
	}
}

func TestProviderAffinityPolicyCapabilityRejectsHomePluginsAndCustomSelectors(t *testing.T) {
	selector := priorityPolicySelector(true)
	defer selector.Stop()
	manager := NewManager(nil, selector, nil)
	if !manager.LocalBuiltinSelector() {
		t.Fatal("local built-in selector rejected")
	}
	manager.SetPluginScheduler(&fakePluginScheduler{})
	if manager.LocalBuiltinSelector() {
		t.Fatal("plugin scheduler reported compatible")
	}
	manager.SetPluginScheduler(nil)
	cfg := &internalconfig.Config{}
	cfg.Home.Enabled = true
	manager.SetConfig(cfg)
	if manager.LocalBuiltinSelector() {
		t.Fatal("Home reported compatible")
	}
	cfg.Home.Enabled = false
	manager.SetConfig(cfg)
	manager.SetSelector(lastAuthSelector{})
	if manager.LocalBuiltinSelector() {
		t.Fatal("custom selector reported compatible")
	}
}

func TestProviderAffinityPolicyTransientPreservationUsesEffectiveFallback(t *testing.T) {
	for _, globalWeighted := range []bool{false, true} {
		var fallback Selector = &FillFirstSelector{}
		var override Selector = &WeightedRoundRobinSelector{}
		if globalWeighted {
			fallback, override = &WeightedRoundRobinSelector{}, &RoundRobinSelector{}
		}
		selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: fallback, RebindOn: SessionAffinityRebindOnQuota,
			ProviderOverrides: map[string]SessionAffinityProviderOverride{"claude": {Fallback: override, PriorityPrecedence: true}}})
		manager := NewManager(nil, selector, nil)
		auth := priorityPolicyAuth("zero-weight", "claude", 4)
		auth.Attributes["weight"] = "0"
		ids := manager.transientlyUnavailableSessionAffinityAuthIDs(selector, []*Auth{auth}, "mixed", "model", time.Now())
		_, retained := ids[auth.ID]
		selector.Stop()
		if retained != globalWeighted {
			t.Fatalf("globalWeighted=%v retained=%v; effective override ignored", globalWeighted, retained)
		}
	}
}
