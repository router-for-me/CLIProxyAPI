package cliproxy

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/apicost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotahistory"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func registerStateAuth(t *testing.T, manager *coreauth.Manager, auth *coreauth.Auth) {
	t.Helper()
	auth.Attributes = map[string]string{"runtime_only": "true"}
	auth.Status = coreauth.StatusActive
	if _, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register(%s): %v", auth.ID, errRegister)
	}
}

func claudeStateSignals(used string, weeklyReset time.Time) map[string]string {
	return map[string]string{
		"Anthropic-Ratelimit-Unified-7d-Reset":       strconv.FormatInt(weeklyReset.Unix(), 10),
		"Anthropic-Ratelimit-Unified-7d-Utilization": used,
	}
}

func newAffinityManager() (*coreauth.Manager, *coreauth.SessionAffinitySelector) {
	selector := coreauth.NewSessionAffinitySelectorWithConfig(coreauth.SessionAffinityConfig{Fallback: coreauth.NewSoonestResetSelector(), TTL: time.Hour})
	return coreauth.NewManager(nil, selector, nil), selector
}

func TestRoutingStatePersisterRoundTripRestoresQuotaHistoryAndBindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "routing-state.json")
	now := time.Now().Truncate(time.Second)
	weeklyReset := now.Add(48 * time.Hour)

	manager, selector := newAffinityManager()
	registerStateAuth(t, manager, &coreauth.Auth{ID: "rs-claude", Provider: "claude",
		Quota: coreauth.QuotaState{ObservedAt: now, Signals: claudeStateSignals("0.4", weeklyReset)}})
	registerStateAuth(t, manager, &coreauth.Auth{ID: "rs-claude-2", Provider: "claude"})
	// Bind a Claude Code session through the real selector path.
	opts := cliproxyexecutor.Options{Headers: map[string][]string{"X-Claude-Code-Session-Id": {"thread-1"}}}
	picked, errPick := selector.Pick(context.Background(), "claude", "claude-sonnet-5-5", opts, manager.List())
	if errPick != nil {
		t.Fatalf("Pick: %v", errPick)
	}

	history := quotahistory.NewStore()
	persister := newRoutingStatePersister(path, manager, history, nil)
	persister.now = func() time.Time { return now }
	persister.tick()
	if _, errStat := os.Stat(path); errStat != nil {
		t.Fatalf("state file not written: %v", errStat)
	}
	// Simulate a restart: fresh manager, selector, and history.
	restarted, restartedSelector := newAffinityManager()
	registerStateAuth(t, restarted, &coreauth.Auth{ID: "rs-claude", Provider: "claude"})
	registerStateAuth(t, restarted, &coreauth.Auth{ID: "rs-claude-2", Provider: "claude"})
	restartedHistory := quotahistory.NewStore()
	loader := newRoutingStatePersister(path, restarted, restartedHistory, nil)
	loader.now = func() time.Time { return now.Add(2 * time.Minute) }
	if errLoad := loader.load(); errLoad != nil {
		t.Fatalf("load: %v", errLoad)
	}

	got, _ := restarted.GetByID("rs-claude")
	if got.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Utilization"] != "0.4" || !got.Quota.ObservedAt.Equal(now) {
		t.Fatalf("restored quota = %+v", got.Quota)
	}
	if window, ok := coreauth.LongWindowQuotaReset(got, now); !ok || !window.ResetAt.Equal(weeklyReset) {
		t.Fatalf("restored weekly reset = %+v/%v, want %v", window, ok, weeklyReset)
	}
	if series := restartedHistory.Series("rs-claude", time.Time{}); len(series) != 1 || *series[0].Windows[0].UsedPercent != 40 {
		t.Fatalf("restored history = %+v", series)
	}
	records := restartedSelector.SnapshotSessionBindings(now.Add(2 * time.Minute))
	if len(records) == 0 || records[0].AuthID != picked.ID {
		t.Fatalf("restored bindings = %+v, want binding to %s", records, picked.ID)
	}
	again, errPick := restartedSelector.Pick(context.Background(), "claude", "claude-sonnet-5-5", opts, restarted.List())
	if errPick != nil || again.ID != picked.ID {
		t.Fatalf("Pick after restart = %v/%v, want %s", again, errPick, picked.ID)
	}
}

func TestRoutingStatePersisterKeepsPendingQuotaForLateAuths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routing-state.json")
	now := time.Now().Truncate(time.Second)
	manager, _ := newAffinityManager()
	registerStateAuth(t, manager, &coreauth.Auth{ID: "late", Provider: "claude",
		Quota: coreauth.QuotaState{ObservedAt: now, Signals: claudeStateSignals("0.2", now.Add(24*time.Hour))}})
	writer := newRoutingStatePersister(path, manager, quotahistory.NewStore(), nil)
	writer.now = func() time.Time { return now }
	writer.tick()

	restarted, _ := newAffinityManager()
	loader := newRoutingStatePersister(path, restarted, quotahistory.NewStore(), nil)
	loader.now = func() time.Time { return now.Add(time.Minute) }
	if errLoad := loader.load(); errLoad != nil {
		t.Fatalf("load: %v", errLoad)
	}
	// Saving before the auth registers must keep the pending snapshot.
	loader.tick()
	registerStateAuth(t, restarted, &coreauth.Auth{ID: "late", Provider: "claude"})
	loader.now = func() time.Time { return now.Add(2 * time.Minute) }
	loader.tick()
	got, _ := restarted.GetByID("late")
	if got.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Utilization"] != "0.2" {
		t.Fatalf("late auth quota = %+v, want restored snapshot", got.Quota)
	}
}

func TestRoutingStatePersisterReappliesQuotaAfterCredentialReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routing-state.json")
	now := time.Now().Truncate(time.Second)
	manager, _ := newAffinityManager()
	registerStateAuth(t, manager, &coreauth.Auth{ID: "cfg-claude", Provider: "claude",
		Quota: coreauth.QuotaState{ObservedAt: now, Signals: claudeStateSignals("0.3", now.Add(24*time.Hour))}})
	writer := newRoutingStatePersister(path, manager, quotahistory.NewStore(), nil)
	writer.now = func() time.Time { return now }
	writer.tick()

	restarted, _ := newAffinityManager()
	registerStateAuth(t, restarted, &coreauth.Auth{ID: "cfg-claude", Provider: "claude"})
	loader := newRoutingStatePersister(path, restarted, quotahistory.NewStore(), nil)
	loader.now = func() time.Time { return now.Add(time.Minute) }
	if errLoad := loader.load(); errLoad != nil {
		t.Fatalf("load: %v", errLoad)
	}
	// The watcher re-synthesizes config-backed credentials after startup.
	replacement := &coreauth.Auth{ID: "cfg-claude", Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"runtime_only": "true"}}
	if _, errUpdate := restarted.Update(coreauth.WithSkipPersist(context.Background()), replacement); errUpdate != nil {
		t.Fatalf("Update: %v", errUpdate)
	}
	if got, _ := restarted.GetByID("cfg-claude"); len(got.Quota.Signals) != 0 {
		t.Fatalf("replacement kept quota %+v; test no longer covers the drop", got.Quota)
	}
	loader.now = func() time.Time { return now.Add(2 * time.Minute) }
	loader.tick()
	got, _ := restarted.GetByID("cfg-claude")
	if got.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Utilization"] != "0.3" {
		t.Fatalf("quota after replacement = %+v, want restored snapshot", got.Quota)
	}
}

func TestRoutingStatePersisterSkipsUnchangedWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routing-state.json")
	manager := coreauth.NewManager(nil, nil, nil)
	persister := newRoutingStatePersister(path, manager, quotahistory.NewStore(), nil)
	base := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	persister.now = func() time.Time { return base }
	persister.tick()
	if errRemove := os.Remove(path); errRemove != nil {
		t.Fatalf("remove: %v", errRemove)
	}
	persister.now = func() time.Time { return base.Add(time.Minute) }
	persister.tick()
	if _, errStat := os.Stat(path); !os.IsNotExist(errStat) {
		t.Fatalf("unchanged state rewritten (stat err %v)", errStat)
	}
}

func TestResolveRoutingStatePath(t *testing.T) {
	got, errResolve := resolveRoutingStatePath("state/routing.json", "/etc/cpa/config.yaml")
	if errResolve != nil || got != filepath.Clean("/etc/cpa/state/routing.json") {
		t.Fatalf("relative = %q/%v", got, errResolve)
	}
	if got, _ := resolveRoutingStatePath("", "/etc/cpa/config.yaml"); got != "" {
		t.Fatalf("empty = %q, want disabled", got)
	}
	if got, _ := resolveRoutingStatePath("/var/lib/x.json", "/etc/cpa/config.yaml"); got != "/var/lib/x.json" {
		t.Fatalf("absolute = %q", got)
	}
}

func TestCarrySessionBindingsKeepsSessionsAcrossSelectorSwap(t *testing.T) {
	manager, selector := newAffinityManager()
	registerStateAuth(t, manager, &coreauth.Auth{ID: "swap-a", Provider: "claude"})
	registerStateAuth(t, manager, &coreauth.Auth{ID: "swap-b", Provider: "claude"})
	opts := cliproxyexecutor.Options{Headers: map[string][]string{"X-Claude-Code-Session-Id": {"thread-swap"}}}
	picked, errPick := selector.Pick(context.Background(), "claude", "claude-sonnet-5-5", opts, manager.List())
	if errPick != nil {
		t.Fatalf("Pick: %v", errPick)
	}
	next := newRoutingSelector(routingRuntimeState{strategy: "round-robin", sessionAffinity: true, sessionAffinityTTL: time.Hour, sessionAffinityRebindOn: coreauth.SessionAffinityRebindOnAny})
	carrySessionBindings(selector, next, time.Now())
	for i := 0; i < 4; i++ {
		got, errNext := next.Pick(context.Background(), "claude", "claude-sonnet-5-5", opts, manager.List())
		if errNext != nil || got.ID != picked.ID {
			t.Fatalf("Pick %d after swap = %v/%v, want %s", i, got, errNext, picked.ID)
		}
	}
}

func TestRoutingStatePersisterRoundTripRestoresAPICost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routing-state.json")
	now := time.Now().UTC().Truncate(time.Hour)
	manager, _ := newAffinityManager()
	cost := apicost.NewStore()
	cost.Import([]apicost.Bucket{{Hour: now, Model: "claude-opus-5-5", Provider: "claude", AuthID: "a1", Requests: 2, InputTokens: 10, OutputTokens: 5, CostUSD: 1.25}})
	writer := newRoutingStatePersister(path, manager, quotahistory.NewStore(), cost)
	writer.now = func() time.Time { return now }
	writer.tick()

	restarted, _ := newAffinityManager()
	restoredCost := apicost.NewStore()
	loader := newRoutingStatePersister(path, restarted, quotahistory.NewStore(), restoredCost)
	loader.now = func() time.Time { return now.Add(time.Minute) }
	if errLoad := loader.load(); errLoad != nil {
		t.Fatalf("load: %v", errLoad)
	}
	buckets := restoredCost.Buckets(now.Add(-time.Hour))
	if len(buckets) != 1 || buckets[0].CostUSD != 1.25 || buckets[0].Requests != 2 || buckets[0].AuthID != "a1" {
		t.Fatalf("restored buckets = %+v", buckets)
	}
}
