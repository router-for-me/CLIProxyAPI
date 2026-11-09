package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestCodexQuotaReserveBoundary(t *testing.T) {
	now := time.Unix(2000, 0)
	for _, test := range []struct {
		name     string
		reserve  any
		provider string
		used     string
		reset    string
		blocked  bool
	}{
		{name: "above reserve", reserve: 5, used: "94.99", reset: "3000"},
		{name: "exactly five percent left", reserve: 5, used: "95", reset: "3000", blocked: true},
		{name: "below five percent left", reserve: 5, used: "99", reset: "3000", blocked: true},
		{name: "exhausted", reserve: 5, used: "100", reset: "3000", blocked: true},
		{name: "JSON number", reserve: json.Number("5"), used: "95", reset: "3000", blocked: true},
		{name: "string setting", reserve: "5", used: "95", reset: "3000", blocked: true},
		{name: "disabled", reserve: 0, used: "100", reset: "3000"},
		{name: "not configured", used: "100", reset: "3000"},
		{name: "negative setting", reserve: -5, used: "100", reset: "3000"},
		{name: "invalid setting", reserve: "NaN", used: "100", reset: "3000"},
		{name: "oversized setting", reserve: 100, used: "100", reset: "3000"},
		{name: "invalid usage", reserve: 5, used: "NaN", reset: "3000"},
		{name: "out of range usage", reserve: 5, used: "101", reset: "3000"},
		{name: "missing usage", reserve: 5, reset: "3000"},
		{name: "missing reset", reserve: 5, used: "95"},
		{name: "expired window", reserve: 5, used: "95", reset: "1999"},
		{name: "reset boundary", reserve: 5, used: "95", reset: "2000"},
		{name: "another provider", reserve: 5, provider: "claude", used: "95", reset: "3000"},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := test.provider
			if provider == "" {
				provider = "codex"
			}
			candidate := &Auth{Provider: provider, Metadata: map[string]any{"quota_reserve_percent": test.reserve}, Quota: QuotaState{
				ObservedAt: now,
				Signals: map[string]string{
					"X-Codex-Primary-Used-Percent": test.used,
					"X-Codex-Primary-Reset-At":     test.reset,
				},
			}}
			blocked, reason, next := isAuthBlockedForModel(candidate, "model", now)
			if blocked != test.blocked {
				t.Fatalf("blocked = %v, want %v", blocked, test.blocked)
			}
			if blocked && (reason != blockReasonCooldown || !next.Equal(time.Unix(3000, 0))) {
				t.Fatalf("reason = %v, next = %v", reason, next)
			}
		})
	}
}

func TestCodexQuotaReserveWindowsAndReset(t *testing.T) {
	now := time.Unix(2000, 0)
	candidate := &Auth{Provider: "codex", Metadata: map[string]any{"quota_reserve_percent": 5}, Quota: QuotaState{
		ObservedAt: time.Unix(1900, 0),
		Signals: map[string]string{
			"X-Codex-Primary-Used-Percent":          "95",
			"X-Codex-Primary-Reset-After-Seconds":   "200",
			"X-Codex-Secondary-Used-Percent":        "96",
			"X-Codex-Secondary-Reset-At":            "3000",
			"X-Codex-Additional-Other-Used-Percent": "100",
			"X-Codex-Additional-Other-Reset-At":     "9000",
		},
	}}
	for _, instant := range []time.Time{now, time.Unix(2100, 0)} {
		blocked, _, next := isAuthBlockedForModel(candidate, "model", instant)
		if !blocked || !next.Equal(time.Unix(3000, 0)) {
			t.Fatalf("at %v: blocked = %v, next = %v", instant, blocked, next)
		}
	}
	if blocked, _, _ := isAuthBlockedForModel(candidate, "model", time.Unix(3000, 0)); blocked {
		t.Fatal("expired snapshots must not keep the account blocked")
	}
	delete(candidate.Quota.Signals, "X-Codex-Secondary-Used-Percent")
	blocked, _, next := isAuthBlockedForModel(candidate, "model", now)
	if !blocked || !next.Equal(time.Unix(2100, 0)) {
		t.Fatalf("relative reset: blocked = %v, next = %v", blocked, next)
	}
	candidate.Quota.Signals["X-Codex-Primary-Used-Percent"] = "20"
	if blocked, _, _ := isAuthBlockedForModel(candidate, "model", now); blocked {
		t.Fatal("a newer healthy reading must release the reserve")
	}
}

func TestCodexQuotaReserveMainBackupSelection(t *testing.T) {
	reset := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	main := &Auth{ID: "main", Provider: "codex", Attributes: map[string]string{"priority": "100"},
		Metadata: map[string]any{"quota_reserve_percent": 5}, Quota: QuotaState{Signals: map[string]string{
			"X-Codex-Secondary-Used-Percent": "94",
			"X-Codex-Secondary-Reset-At":     reset,
		}}}
	backup := &Auth{ID: "backup", Provider: "codex", Metadata: map[string]any{"quota_reserve_percent": 5}}
	selector := &RoundRobinSelector{}
	ctx := context.Background()
	for range 3 {
		selected, errPick := selector.Pick(ctx, "codex", "model", cliproxyexecutor.Options{}, []*Auth{main, backup})
		if errPick != nil || selected != main {
			t.Fatalf("healthy main: selected = %v, error = %v", selected, errPick)
		}
	}
	main.Quota.Signals["X-Codex-Secondary-Used-Percent"] = "95"
	selected, errPick := selector.Pick(ctx, "codex", "model", cliproxyexecutor.Options{}, []*Auth{main, backup})
	if errPick != nil || selected != backup {
		t.Fatalf("reserve reached: selected = %v, error = %v", selected, errPick)
	}
	backup.Quota = main.Quota.Clone()
	if _, errPick = selector.Pick(ctx, "codex", "model", cliproxyexecutor.Options{}, []*Auth{main, backup}); errPick == nil {
		t.Fatal("both accounts at reserve must not spend the remaining allowance")
	}
	main.Quota.Signals["X-Codex-Secondary-Reset-At"] = "1"
	selected, errPick = selector.Pick(ctx, "codex", "model", cliproxyexecutor.Options{}, []*Auth{main, backup})
	if errPick != nil || selected != main {
		t.Fatalf("main reset: selected = %v, error = %v", selected, errPick)
	}
}

func TestCodexQuotaReserveSchedulerUpdatesAllModels(t *testing.T) {
	models := []string{"quota-reserve-first", "quota-reserve-second"}
	main := &Auth{ID: "quota-reserve-main", Provider: "codex", Generation: 1,
		Attributes: map[string]string{"priority": "100"}, Metadata: map[string]any{"quota_reserve_percent": 5}}
	backup := &Auth{ID: "quota-reserve-backup", Provider: "codex", Generation: 1,
		Metadata: map[string]any{"quota_reserve_percent": 5}}
	reg := registry.GetGlobalRegistry()
	for _, candidate := range []*Auth{main, backup} {
		reg.RegisterClient(candidate.ID, "codex", []*registry.ModelInfo{{ID: models[0]}, {ID: models[1]}})
		t.Cleanup(func() { reg.UnregisterClient(candidate.ID) })
	}
	scheduler := newSchedulerForTest(&RoundRobinSelector{}, main, backup)
	checkSelection := func(want string) {
		t.Helper()
		for _, model := range models {
			selected, errPick := scheduler.pickSingle(context.Background(), "codex", model, cliproxyexecutor.Options{}, nil)
			if errPick != nil || selected == nil || selected.ID != want {
				t.Fatalf("%s: selected = %v, error = %v, want %s", model, selected, errPick, want)
			}
		}
	}
	checkSelection(main.ID)
	main = main.Clone()
	main.Generation++
	reset := time.Now().Add(time.Hour).Truncate(time.Second)
	main.Quota.Signals = map[string]string{
		"X-Codex-Primary-Used-Percent": "95",
		"X-Codex-Primary-Reset-At":     strconv.FormatInt(reset.Unix(), 10),
	}
	scheduler.upsertAuthResult(main, models[:1], false)
	checkSelection(backup.ID)
	main = main.Clone()
	main.Generation++
	reset = reset.Add(time.Hour)
	main.Quota.Signals["X-Codex-Primary-Reset-At"] = strconv.FormatInt(reset.Unix(), 10)
	scheduler.upsertAuthResult(main, models[:1], false)
	for _, model := range models {
		entry := scheduler.providers["codex"].modelShards[model].entries[main.ID]
		if !entry.nextRetryAt.Equal(reset) {
			t.Fatalf("%s retry = %v, want %v", model, entry.nextRetryAt, reset)
		}
	}
	main = main.Clone()
	main.Generation++
	main.Quota.Signals["X-Codex-Primary-Used-Percent"] = "20"
	scheduler.upsertAuthResult(main, models[:1], false)
	checkSelection(main.ID)
}

func TestCodexQuotaReserveCooldownView(t *testing.T) {
	now := time.Unix(2000, 0)
	candidate := &Auth{Provider: "codex", Metadata: map[string]any{"quota_reserve_percent": 5}, Quota: QuotaState{
		Signals: map[string]string{
			"X-Codex-Secondary-Used-Percent": "95",
			"X-Codex-Secondary-Reset-At":     "3000",
		},
	}}
	views := CooldownSnapshotForAuth(candidate, now)
	if len(views) != 1 || views[0].Scope != "credential" || views[0].Reason != "quota_reserve" || views[0].RemainingSeconds != 1000 {
		t.Fatalf("reserve cooldown = %#v", views)
	}
	if candidate.Quota.Exceeded || candidate.Disabled || candidate.Unavailable {
		t.Fatal("reserve projection must not change upstream or operator state")
	}
	if views = CooldownSnapshotForAuth(candidate, time.Unix(3000, 0)); len(views) != 0 {
		t.Fatalf("expired reserve = %#v", views)
	}
}

func TestCodexQuotaReserveRebindsStickySession(t *testing.T) {
	main := &Auth{ID: "reserve-sticky-main", Provider: "codex", Attributes: map[string]string{"priority": "100"},
		Metadata: map[string]any{"quota_reserve_percent": 5}}
	backup := &Auth{ID: "reserve-sticky-backup", Provider: "codex", Metadata: map[string]any{"quota_reserve_percent": 5}}
	selector := NewSessionAffinitySelector(&RoundRobinSelector{})
	opts := cliproxyexecutor.Options{Headers: http.Header{"Session_id": []string{"reserve-session"}}}
	auths := []*Auth{main, backup}
	selected, errPick := selector.Pick(context.Background(), "codex", "model", opts, auths)
	if errPick != nil || selected != main {
		t.Fatalf("initial binding: selected = %v, error = %v", selected, errPick)
	}
	main.Quota.Signals = map[string]string{
		"X-Codex-Primary-Used-Percent": "95",
		"X-Codex-Primary-Reset-At":     strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
	}
	selected, errPick = selector.Pick(context.Background(), "codex", "model", opts, auths)
	if errPick != nil || selected != backup {
		t.Fatalf("reserve must override affinity: selected = %v, error = %v", selected, errPick)
	}
}
