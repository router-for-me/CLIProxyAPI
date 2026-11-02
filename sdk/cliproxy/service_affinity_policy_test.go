package cliproxy

import (
	"context"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestProviderAffinityRuntimeStateIsCanonical(t *testing.T) {
	first := &internalconfig.Config{Routing: internalconfig.RoutingConfig{Strategy: "fill-first", SessionAffinity: true,
		SessionAffinityOverrides: map[string]internalconfig.SessionAffinityOverride{
			"claude": {Strategy: "rr", Precedence: "priority"}, "gemini": {Strategy: "ff"},
		}}}
	second := &internalconfig.Config{Routing: internalconfig.RoutingConfig{Strategy: "fill-first", SessionAffinity: true,
		SessionAffinityOverrides: map[string]internalconfig.SessionAffinityOverride{
			"gemini": {Strategy: "fill-first", Precedence: "session"}, "ANTHROPIC": {Strategy: "round-robin", Precedence: "priority"},
		}}}
	a, b := normalizedRoutingRuntimeState(first), normalizedRoutingRuntimeState(second)
	if a != b {
		t.Fatalf("equivalent override state differs: %+v vs %+v", a, b)
	}
	second.Routing.SessionAffinityOverrides["ANTHROPIC"] = internalconfig.SessionAffinityOverride{Precedence: "session"}
	if a == normalizedRoutingRuntimeState(second) {
		t.Fatal("effective change omitted from runtime equality")
	}
}

func TestProviderAffinityRuntimeStateIgnoresNoOpOverrides(t *testing.T) {
	base := &internalconfig.Config{Routing: internalconfig.RoutingConfig{Strategy: "fill-first", SessionAffinity: true}}
	state := normalizedRoutingRuntimeState(base)
	base.Routing.SessionAffinityOverrides = map[string]internalconfig.SessionAffinityOverride{"claude": {}}
	if got := normalizedRoutingRuntimeState(base); got != state {
		t.Fatal("empty inherited override resets affinity")
	}
	base.Routing.SessionAffinityOverrides["claude"] = internalconfig.SessionAffinityOverride{Strategy: "ff", Precedence: "session"}
	if got := normalizedRoutingRuntimeState(base); got != state {
		t.Fatal("explicit global-equivalent override resets affinity")
	}
	base.Routing.SessionAffinityOverrides["claude"] = internalconfig.SessionAffinityOverride{Precedence: "priority"}
	inherited := normalizedRoutingRuntimeState(base)
	base.Routing.SessionAffinityOverrides["claude"] = internalconfig.SessionAffinityOverride{Strategy: "fill-first", Precedence: "priority"}
	if got := normalizedRoutingRuntimeState(base); got != inherited {
		t.Fatal("equivalent priority override resets affinity")
	}
}

func TestProviderAffinityRuntimeSelectorUsesOverrides(t *testing.T) {
	cfg := &internalconfig.Config{Routing: internalconfig.RoutingConfig{Strategy: "fill-first", SessionAffinity: true,
		SessionAffinityOverrides: map[string]internalconfig.SessionAffinityOverride{"claude": {Strategy: "round-robin", Precedence: "priority"}}}}
	selector, ok := newRoutingSelector(normalizedRoutingRuntimeState(cfg)).(*coreauth.SessionAffinitySelector)
	if !ok {
		t.Fatal("lost concrete session-affinity selector")
	}
	defer selector.Stop()
	auths := []*coreauth.Auth{{ID: "a", Provider: "claude"}, {ID: "b", Provider: "claude"}}
	seen := map[string]bool{}
	for _, session := range []string{"first", "second"} {
		picked, err := selector.Pick(context.Background(), "claude", "model", cliproxyexecutor.Options{
			Headers: http.Header{"X-Claude-Code-Session-Id": []string{session}}}, auths)
		if err != nil || picked == nil {
			t.Fatalf("Pick() = %v, %v", picked, err)
		}
		seen[picked.ID] = true
	}
	if len(seen) != 2 {
		t.Fatalf("override did not distribute sessions: %v", seen)
	}
}

func TestProviderAffinityInvalidConfigDoesNotReplaceCurrent(t *testing.T) {
	original := &internalconfig.Config{}
	service := &Service{cfg: original}
	invalid := &internalconfig.Config{Routing: internalconfig.RoutingConfig{
		SessionAffinityOverrides: map[string]internalconfig.SessionAffinityOverride{"claude": {Precedence: "invalid"}}}}
	if commit := service.commitConfigUpdate(invalid); commit.cfg != nil {
		t.Fatal("invalid policy accepted")
	}
	if service.currentConfig() != original {
		t.Fatal("invalid policy replaced config")
	}
}
