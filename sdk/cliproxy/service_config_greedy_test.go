package cliproxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestGreedyRoutingWiring(t *testing.T) {
	state := normalizedRoutingRuntimeState(&internalconfig.Config{Routing: internalconfig.RoutingConfig{Strategy: " GREEDY "}})
	if state.strategy != "greedy" {
		t.Fatal(state.strategy)
	}
	if _, ok := newRoutingSelector(state).(*coreauth.GreedySelector); !ok {
		t.Fatal("greedy not selected")
	}
	state.sessionAffinity = true
	affinity, ok := newRoutingSelector(state).(*coreauth.SessionAffinitySelector)
	if !ok {
		t.Fatal("affinity lost")
	}
	defer affinity.Stop()
	now := time.Now()
	a := &coreauth.Auth{ID: "a", Provider: "claude", QuotaResetSchedule: coreauth.QuotaResetSchedule{WeeklyResetAt: now.Add(2 * time.Hour), ObservedAt: now}}
	b := &coreauth.Auth{ID: "b", Provider: "claude", QuotaResetSchedule: coreauth.QuotaResetSchedule{WeeklyResetAt: now.Add(time.Hour), ObservedAt: now}}
	opts := cliproxyexecutor.Options{Headers: http.Header{"X-Claude-Code-Session-Id": []string{"session-one"}}}
	got, err := affinity.Pick(context.Background(), "claude", "", opts, []*coreauth.Auth{a, b})
	if err != nil || got.ID != "b" {
		t.Fatalf("affinity fallback not greedy: %v %v", got, err)
	}
	a.QuotaResetSchedule.WeeklyResetAt = now.Add(30 * time.Minute)
	got, err = affinity.Pick(context.Background(), "claude", "", opts, []*coreauth.Auth{a, b})
	if err != nil || got.ID != "b" {
		t.Fatalf("existing session moved: %v %v", got, err)
	}
	opts.Headers.Set("X-Claude-Code-Session-Id", "session-two")
	got, err = affinity.Pick(context.Background(), "claude", "", opts, []*coreauth.Auth{a, b})
	if err != nil || got.ID != "a" {
		t.Fatalf("new session ignored updated schedule: %v %v", got, err)
	}
	if _, ok := newRoutingSelector(normalizedRoutingRuntimeState(nil)).(*coreauth.RoundRobinSelector); !ok {
		t.Fatal("default changed")
	}
}
