package auth

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

var sequentialTestNow = time.Unix(1_000_000, 0)

func sequentialCodexAuth(id string, priority int) *Auth {
	auth := &Auth{ID: id, Provider: "codex", Status: StatusActive, Quota: QuotaState{Signals: map[string]string{}}}
	if priority != 0 {
		auth.Attributes = map[string]string{"priority": strconv.Itoa(priority)}
	}
	return auth
}

// setCodexUsage records a Codex reading for the primary (5-hour) or secondary (weekly) window.
func setCodexUsage(auth *Auth, window string, used float64, reset time.Time) {
	auth.Quota.Signals["X-Codex-"+window+"-Used-Percent"] = strconv.FormatFloat(used, 'f', -1, 64)
	auth.Quota.Signals["X-Codex-"+window+"-Reset-At"] = strconv.FormatInt(reset.Unix(), 10)
}

func newSequentialTestSelector(reserve float64, statePath string) (*SequentialSelector, *time.Time) {
	now := sequentialTestNow
	selector := NewSequentialSelector(reserve, statePath)
	selector.now = func() time.Time { return now }
	return selector, &now
}

func pickSequential(t *testing.T, selector *SequentialSelector, pool []*Auth, allowed ...*Auth) string {
	t.Helper()
	if len(allowed) == 0 {
		allowed = pool
	}
	selected, errPick := selector.Pick(withSequentialPool(context.Background(), pool), "codex", "model", cliproxyexecutor.Options{}, allowed)
	if errPick != nil {
		t.Fatalf("pick: %v", errPick)
	}
	return selected.ID
}

func expectSequential(t *testing.T, got, want, step string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: selected %s, want %s", step, got, want)
	}
}

func TestSequentialFillsAccountsInOrder(t *testing.T) {
	selector, _ := newSequentialTestSelector(5, "")
	a, b, c := sequentialCodexAuth("a", 0), sequentialCodexAuth("b", 0), sequentialCodexAuth("c", 0)
	pool := []*Auth{c, a, b}
	reset := sequentialTestNow.Add(5 * time.Hour)

	expectSequential(t, pickSequential(t, selector, pool), "a", "start")
	setCodexUsage(a, "Primary", 94.9, reset)
	expectSequential(t, pickSequential(t, selector, pool), "a", "a below 95%")
	setCodexUsage(a, "Primary", 95, reset)
	expectSequential(t, pickSequential(t, selector, pool), "b", "a at 95%")
	setCodexUsage(b, "Secondary", 97, reset)
	expectSequential(t, pickSequential(t, selector, pool), "c", "b weekly at 97%")
}

func TestSequentialResetDoesNotPullTrafficBack(t *testing.T) {
	selector, now := newSequentialTestSelector(5, "")
	a, b := sequentialCodexAuth("a", 100), sequentialCodexAuth("b", 0)
	pool := []*Auth{a, b}
	setCodexUsage(a, "Primary", 96, sequentialTestNow.Add(time.Hour))
	setCodexUsage(b, "Primary", 40, sequentialTestNow.Add(5*time.Hour))

	expectSequential(t, pickSequential(t, selector, pool), "b", "a at reserve")
	*now = sequentialTestNow.Add(2 * time.Hour) // a's window has reset; b still has room
	expectSequential(t, pickSequential(t, selector, pool), "b", "after a reset")
	setCodexUsage(b, "Primary", 95, sequentialTestNow.Add(5*time.Hour))
	expectSequential(t, pickSequential(t, selector, pool), "a", "b at reserve")
}

func TestSequentialWrapsToAccountThatReset(t *testing.T) {
	selector, now := newSequentialTestSelector(5, "")
	a, b, c := sequentialCodexAuth("a", 0), sequentialCodexAuth("b", 0), sequentialCodexAuth("c", 0)
	pool := []*Auth{a, b, c}
	setCodexUsage(a, "Primary", 95, sequentialTestNow.Add(time.Hour))
	setCodexUsage(b, "Primary", 95, sequentialTestNow.Add(10*time.Hour))
	expectSequential(t, pickSequential(t, selector, pool), "c", "a and b at reserve")
	*now = sequentialTestNow.Add(2 * time.Hour)
	expectSequential(t, pickSequential(t, selector, pool), "c", "a reset while c has room")
	setCodexUsage(c, "Primary", 99, sequentialTestNow.Add(10*time.Hour))
	expectSequential(t, pickSequential(t, selector, pool), "a", "c at reserve wraps to a")
}

func TestSequentialSpendsReservesInOrder(t *testing.T) {
	selector, now := newSequentialTestSelector(5, "")
	a, b, c := sequentialCodexAuth("a", 0), sequentialCodexAuth("b", 0), sequentialCodexAuth("c", 0)
	pool := []*Auth{a, b, c}
	for _, auth := range pool {
		setCodexUsage(auth, "Primary", 96, sequentialTestNow.Add(5*time.Hour))
	}
	setCodexUsage(b, "Primary", 96, sequentialTestNow.Add(time.Hour))

	expectSequential(t, pickSequential(t, selector, pool), "a", "all at reserve")
	setCodexUsage(a, "Primary", 100, sequentialTestNow.Add(5*time.Hour))
	expectSequential(t, pickSequential(t, selector, pool), "b", "a used up")
	// a is now cooling down after its 429 and is no longer offered.
	expectSequential(t, pickSequential(t, selector, pool, b, c), "b", "b reserve")
	*now = sequentialTestNow.Add(2 * time.Hour) // b's window resets
	expectSequential(t, pickSequential(t, selector, pool, b, c), "b", "b reset")
	setCodexUsage(b, "Primary", 100, sequentialTestNow.Add(7*time.Hour))
	expectSequential(t, pickSequential(t, selector, pool, c), "c", "b used up")
}

// Seen live on 2026-11-13: with every account at the reserve, requests the
// current account could not take moved the pool, and the next request moved it
// back, every few seconds.
func TestSequentialAllAtReserveDoesNotBounce(t *testing.T) {
	selector, _ := newSequentialTestSelector(5, "")
	a, b := sequentialCodexAuth("a", 0), sequentialCodexAuth("b", 0)
	pool := []*Auth{a, b}
	setCodexUsage(a, "Primary", 96, sequentialTestNow.Add(5*time.Hour))
	setCodexUsage(b, "Primary", 96, sequentialTestNow.Add(5*time.Hour))
	expectSequential(t, pickSequential(t, selector, pool), "a", "all at reserve")
	setCodexUsage(a, "Primary", 97, sequentialTestNow.Add(5*time.Hour))
	setCodexUsage(b, "Primary", 96, sequentialTestNow.Add(5*time.Hour))
	expectSequential(t, pickSequential(t, selector, pool, b), "b", "a cannot take this request")
	for _, current := range selector.Current() {
		expectSequential(t, current, "a", "pool stays on a")
	}
	expectSequential(t, pickSequential(t, selector, pool), "a", "a still has reserve")
}

func TestSequentialResetDuringReservesMovesOn(t *testing.T) {
	selector, now := newSequentialTestSelector(5, "")
	a, b := sequentialCodexAuth("a", 0), sequentialCodexAuth("b", 0)
	pool := []*Auth{a, b}
	setCodexUsage(a, "Primary", 97, sequentialTestNow.Add(5*time.Hour))
	setCodexUsage(b, "Primary", 97, sequentialTestNow.Add(time.Hour))
	expectSequential(t, pickSequential(t, selector, pool), "a", "spending a's reserve")
	*now = sequentialTestNow.Add(2 * time.Hour)
	expectSequential(t, pickSequential(t, selector, pool), "b", "b reset")
	*now = sequentialTestNow.Add(6 * time.Hour) // a resets too, but b still has room
	expectSequential(t, pickSequential(t, selector, pool), "b", "stay on b")
}

func TestSequentialUnavailableCurrentKeepsPool(t *testing.T) {
	selector, _ := newSequentialTestSelector(5, "")
	a, b, c := sequentialCodexAuth("a", 0), sequentialCodexAuth("b", 0), sequentialCodexAuth("c", 0)
	pool := []*Auth{a, b, c}
	expectSequential(t, pickSequential(t, selector, pool), "a", "start")
	// a has room but is cooling down after a short rate limit, or was already tried.
	expectSequential(t, pickSequential(t, selector, pool, b, c), "b", "a unavailable")
	expectSequential(t, pickSequential(t, selector, pool), "a", "a back")

	// a hits its limit: its 429 carries a reading at the limit, so the pool moves.
	setCodexUsage(a, "Primary", 100, sequentialTestNow.Add(time.Hour))
	expectSequential(t, pickSequential(t, selector, pool, b, c), "b", "a limited")
	expectSequential(t, pickSequential(t, selector, pool), "b", "pool stays on b")
}

func TestSequentialOrdersByPriorityThenID(t *testing.T) {
	selector, _ := newSequentialTestSelector(5, "")
	low, high := sequentialCodexAuth("a-low", 0), sequentialCodexAuth("z-high", 10)
	expectSequential(t, pickSequential(t, selector, []*Auth{low, high}), "z-high", "priority first")
}

func TestSequentialReadsClaudeWindows(t *testing.T) {
	selector, _ := newSequentialTestSelector(5, "")
	reset := strconv.FormatInt(sequentialTestNow.Add(time.Hour).Unix(), 10)
	claude := func(id, util5h, util7d, reset7d string) *Auth {
		return &Auth{ID: id, Provider: "claude", Quota: QuotaState{Signals: map[string]string{
			"Anthropic-Ratelimit-Unified-5h-Utilization": util5h,
			"Anthropic-Ratelimit-Unified-5h-Reset":       reset,
			"Anthropic-Ratelimit-Unified-7d-Utilization": util7d,
			"Anthropic-Ratelimit-Unified-7d-Reset":       reset7d,
		}}}
	}
	rfc := sequentialTestNow.Add(time.Hour).UTC().Format(time.RFC3339)
	for _, test := range []struct {
		name string
		auth *Auth
		room bool
	}{
		{"both low", claude("x", "0.10", "0.94", reset), true},
		{"weekly at 95%", claude("x", "0.10", "0.95", reset), false},
		{"over the limit", claude("x", "1.02", "0.10", reset), false},
		{"RFC 3339 reset", claude("x", "0.10", "0.96", rfc), false},
		{"weekly without reset", claude("x", "0.10", "0.99", ""), true},
		{"expired weekly", claude("x", "0.10", "0.99", strconv.FormatInt(sequentialTestNow.Unix(), 10)), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := selector.hasHeadroom(test.auth, sequentialTestNow); got != test.room {
				t.Fatalf("headroom = %v, want %v", got, test.room)
			}
		})
	}
}

func TestSequentialCodexRelativeReset(t *testing.T) {
	selector, _ := newSequentialTestSelector(5, "")
	auth := sequentialCodexAuth("a", 0)
	auth.Quota.ObservedAt = sequentialTestNow.Add(-time.Minute)
	auth.Quota.Signals["X-Codex-Primary-Used-Percent"] = "96"
	auth.Quota.Signals["X-Codex-Primary-Reset-After-Seconds"] = "120"
	if selector.hasHeadroom(auth, sequentialTestNow) {
		t.Fatal("window that resets in a minute must still count")
	}
	if !selector.hasHeadroom(auth, sequentialTestNow.Add(2*time.Minute)) {
		t.Fatal("window past its relative reset must not count")
	}
}

func TestSequentialZeroReserveSwitchesAtLimit(t *testing.T) {
	selector, _ := newSequentialTestSelector(0, "")
	a, b := sequentialCodexAuth("a", 0), sequentialCodexAuth("b", 0)
	pool := []*Auth{a, b}
	setCodexUsage(a, "Primary", 99, sequentialTestNow.Add(time.Hour))
	expectSequential(t, pickSequential(t, selector, pool), "a", "99% with no reserve")
	setCodexUsage(a, "Primary", 100, sequentialTestNow.Add(time.Hour))
	expectSequential(t, pickSequential(t, selector, pool), "b", "100% with no reserve")
}

func TestSequentialInvalidReserveUsesDefault(t *testing.T) {
	for _, reserve := range []float64{-1, 100, 150} {
		if got := NewSequentialSelector(reserve, "").reserve; got != DefaultSequentialReservePercent {
			t.Fatalf("reserve %v became %v", reserve, got)
		}
	}
}

func TestSequentialCurrentAccountSurvivesRestart(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "sequential-routing.state")
	a, b := sequentialCodexAuth("a", 0), sequentialCodexAuth("b", 0)
	pool := []*Auth{a, b}
	setCodexUsage(a, "Primary", 95, sequentialTestNow.Add(time.Hour))
	first, _ := newSequentialTestSelector(5, statePath)
	expectSequential(t, pickSequential(t, first, pool), "b", "before restart")

	setCodexUsage(a, "Primary", 0, sequentialTestNow.Add(5*time.Hour)) // a has reset
	restarted, _ := newSequentialTestSelector(5, statePath)
	expectSequential(t, pickSequential(t, restarted, pool), "b", "after restart")
}

func TestSequentialShouldRelease(t *testing.T) {
	selector, _ := newSequentialTestSelector(5, "")
	a, b := sequentialCodexAuth("a", 0), sequentialCodexAuth("b", 0)
	setCodexUsage(a, "Primary", 96, sequentialTestNow.Add(time.Hour))
	if !selector.ShouldRelease(a, []*Auth{b}) {
		t.Fatal("session on an account at the reserve must leave while another has room")
	}
	if selector.ShouldRelease(b, []*Auth{a}) {
		t.Fatal("session on an account with room must stay")
	}
	setCodexUsage(b, "Primary", 97, sequentialTestNow.Add(time.Hour))
	if selector.ShouldRelease(a, []*Auth{b}) {
		t.Fatal("with every account at the reserve there is nowhere better to go")
	}
}

func TestSequentialManagerUsesWholePool(t *testing.T) {
	model := "sequential-manager-model"
	selector := NewSequentialSelector(5, "")
	manager := NewManager(nil, selector, nil)
	manager.RegisterExecutor(&issue6416TestExecutor{id: "codex"})
	reset := time.Now().Add(time.Hour)
	main := sequentialCodexAuth("sequential-main", 100)
	setCodexUsage(main, "Secondary", 96, reset)
	backup := sequentialCodexAuth("sequential-backup", 0)
	for _, auth := range []*Auth{main, backup} {
		registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatal(errRegister)
		}
	}
	ctx := context.Background()
	selected, errSelect := manager.SelectAuth(ctx, "codex", model, cliproxyexecutor.Options{})
	if errSelect != nil || selected.ID != backup.ID {
		t.Fatalf("main at reserve: selected %v, error %v", selected, errSelect)
	}
	// A retry that excludes the backup spends the main's reserve for that request only.
	retry, _, errRetry := manager.pickNextLegacy(ctx, "codex", model, cliproxyexecutor.Options{}, map[string]struct{}{backup.ID: {}})
	if errRetry != nil || retry.ID != main.ID {
		t.Fatalf("retry: selected %v, error %v", retry, errRetry)
	}
	selected, errSelect = manager.SelectAuth(ctx, "codex", model, cliproxyexecutor.Options{})
	if errSelect != nil || selected.ID != backup.ID {
		t.Fatalf("after retry: selected %v, error %v", selected, errSelect)
	}
	if !manager.ShouldReleasePinnedAuth(main.ID, model) {
		t.Fatal("a session pinned to the main must be released")
	}
	if manager.ShouldReleasePinnedAuth(backup.ID, model) {
		t.Fatal("a session pinned to the backup must stay")
	}
}

func TestShouldReleasePinnedAuthIgnoresOtherStrategies(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	main := sequentialCodexAuth("release-other-main", 0)
	setCodexUsage(main, "Primary", 99, time.Now().Add(time.Hour))
	if _, errRegister := manager.Register(context.Background(), main); errRegister != nil {
		t.Fatal(errRegister)
	}
	if manager.ShouldReleasePinnedAuth(main.ID, "") {
		t.Fatal("round-robin must not release pins")
	}
}

func TestSequentialManagerMovesPoolWhenCurrentIsUnavailable(t *testing.T) {
	model := "sequential-manager-unavailable-model"
	selector, now := newSequentialTestSelector(5, "")
	manager := NewManager(nil, selector, nil)
	manager.RegisterExecutor(&issue6416TestExecutor{id: "codex"})
	first, second, third := sequentialCodexAuth("seq-u-1", 0), sequentialCodexAuth("seq-u-2", 0), sequentialCodexAuth("seq-u-3", 0)
	setCodexUsage(first, "Primary", 96, sequentialTestNow.Add(10*time.Hour))
	for _, auth := range []*Auth{first, second, third} {
		registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatal(errRegister)
		}
	}
	ctx := context.Background()
	pick := func(tried map[string]struct{}) string {
		t.Helper()
		selected, _, errPick := manager.pickNextLegacy(ctx, "codex", model, cliproxyexecutor.Options{}, tried)
		if errPick != nil {
			t.Fatal(errPick)
		}
		return selected.ID
	}
	expectSequential(t, pick(nil), second.ID, "first at reserve")

	// The second account's failed request reports it at its limit, and it is not offered again.
	updated, _ := manager.GetByID(second.ID)
	setCodexUsage(updated, "Primary", 100, sequentialTestNow.Add(time.Hour))
	if _, errUpdate := manager.Update(ctx, updated); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	expectSequential(t, pick(map[string]struct{}{second.ID: {}}), third.ID, "second limited")

	// Once the second account resets, the pool must stay on the third.
	*now = sequentialTestNow.Add(2 * time.Hour)
	expectSequential(t, pick(nil), third.ID, "after second resets")
}

func TestSequentialCurrentReportsSavedAccountBeforeAnyPick(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "sequential-routing.state")
	a, b := sequentialCodexAuth("a", 0), sequentialCodexAuth("b", 0)
	setCodexUsage(a, "Primary", 95, sequentialTestNow.Add(time.Hour))
	first, _ := newSequentialTestSelector(5, statePath)
	if got := first.Current(); len(got) != 0 {
		t.Fatalf("current before any pick = %v, want empty", got)
	}
	expectSequential(t, pickSequential(t, first, []*Auth{a, b}), "b", "first pick")

	restarted, _ := newSequentialTestSelector(5, statePath)
	if got := restarted.Current(); len(got) != 1 || got["codex"] != "b" {
		t.Fatalf("current after restart = %v, want codex: b", got)
	}
}
