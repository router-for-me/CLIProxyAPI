package ignite

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/router"
)

type fakeHost struct {
	mu       sync.Mutex
	accounts []router.AccountView
	results  []Result
	fired    []string
	stamp    string
	refresh  int
}

func (h *fakeHost) Accounts() []router.AccountView {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]router.AccountView(nil), h.accounts...)
}
func (h *fakeHost) RequestRefresh(string) { h.mu.Lock(); h.refresh++; h.mu.Unlock() }
func (h *fakeHost) Models(string) []string {
	return []string{"claude-opus-5-5", "claude-haiku-4-5", "claude-haiku-5-5", "gpt-6-luna", "gpt-6"}
}
func (h *fakeHost) CredentialStamp(string) string { h.mu.Lock(); defer h.mu.Unlock(); return h.stamp }
func (h *fakeHost) Fire(_ context.Context, _, _, model string) Result {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fired = append(h.fired, model)
	if len(h.results) == 0 {
		return Result{}
	}
	r := h.results[0]
	h.results = h.results[1:]
	return r
}

func (h *fakeHost) firedCount() int { h.mu.Lock(); defer h.mu.Unlock(); return len(h.fired) }

func ptr(t time.Time) *time.Time { return &t }

// at returns 2026-11-27 at the given local hour/minute in UTC.
func at(hour, minute int) time.Time { return time.Date(2026, 11, 27, hour, minute, 0, 0, time.UTC) }

func testConfig() Config {
	c := Default()
	c.Enabled = true
	c.Timezone = "UTC"
	if err := c.validate(); err != nil {
		panic(err)
	}
	return c
}

func claude(id string, w ...router.WindowView) router.AccountView {
	return router.AccountView{ID: id, Provider: "claude", Present: true, Windows: w}
}

func five(used float64, resets *time.Time, observed time.Time) router.WindowView {
	return router.WindowView{Key: "five_hour", Kind: "five_hour", Used: used, Remaining: 100 - used, ResetsAt: resets, ObservedAt: observed}
}

func weekly(remaining float64) router.WindowView {
	return router.WindowView{Key: "weekly", Kind: "weekly", Used: 100 - remaining, Remaining: remaining}
}

func newTest(h *fakeHost, now *time.Time) *Igniter {
	g := New(h, testConfig())
	g.Now = func() time.Time { return *now }
	return g
}

func (g *Igniter) wait() { g.wg.Wait() }

func TestClassify(t *testing.T) {
	now := at(12, 0)
	cases := []struct {
		name string
		a    router.AccountView
		want windowState
	}{
		{"claude idle no reset", claude("a", five(0, nil, now)), windowIdle},
		{"claude reset passed", claude("a", five(40, ptr(now.Add(-time.Minute)), now.Add(-10*time.Minute))), windowIdle},
		{"claude active", claude("a", five(10, ptr(now.Add(time.Hour)), now)), windowActive},
		{"no five hour", claude("a", weekly(50)), windowUnknown},
		{"codex rolling idle", router.AccountView{Provider: "codex", Windows: []router.WindowView{five(0, ptr(now.Add(-time.Minute).Add(fiveHour)), now.Add(-time.Minute))}}, windowIdle},
		{"codex active", router.AccountView{Provider: "codex", Windows: []router.WindowView{five(3, ptr(now.Add(2*time.Hour)), now)}}, windowActive},
	}
	for _, c := range cases {
		if got, _ := classify(c.a, now); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestIgnitesIdleWindowInsideHours(t *testing.T) {
	now := at(9, 0)
	h := &fakeHost{accounts: []router.AccountView{claude("a", five(0, nil, now), weekly(80))}}
	g := newTest(h, &now)
	g.Tick()
	g.wait()
	if h.firedCount() != 1 || h.fired[0] != "claude-haiku-5-5" {
		t.Fatalf("expected newest haiku fired once, got %v", h.fired)
	}
	// Confirmation: a poll shows the new window.
	now = now.Add(20 * time.Second)
	h.accounts = []router.AccountView{claude("a", five(1, ptr(now.Add(fiveHour)), now), weekly(80))}
	g.Tick()
	ev := g.Events(1)
	if len(ev) != 1 || ev[0].Kind != EventSuccess {
		t.Fatalf("expected success event, got %+v", ev)
	}
}

func TestWaitsOutsideHoursAndForReset(t *testing.T) {
	now := at(23, 0)
	h := &fakeHost{accounts: []router.AccountView{claude("a", five(0, nil, now))}}
	g := newTest(h, &now)
	g.Tick()
	g.wait()
	if h.firedCount() != 0 {
		t.Fatal("must not fire after end hour")
	}
	now = at(10, 0)
	h.accounts = []router.AccountView{claude("a", five(50, ptr(at(10, 30)), now))}
	g.Tick()
	g.wait()
	if h.firedCount() != 0 {
		t.Fatal("must not fire while window active")
	}
	now = at(10, 30).Add(2 * time.Second)
	g.Tick()
	g.wait()
	if h.firedCount() != 0 {
		t.Fatal("must wait grace seconds after reset")
	}
	now = at(10, 30).Add(4 * time.Second)
	g.Tick()
	g.wait()
	if h.firedCount() != 1 {
		t.Fatalf("expected fire after reset+grace, got %d", h.firedCount())
	}
}

func TestSkipsWhenWeeklyExhausted(t *testing.T) {
	now := at(9, 0)
	h := &fakeHost{accounts: []router.AccountView{claude("a", five(0, nil, now), weekly(0))}}
	g := newTest(h, &now)
	g.Tick()
	g.wait()
	if h.firedCount() != 0 {
		t.Fatal("must not ignite with weekly exhausted")
	}
}

func TestFailureHandling(t *testing.T) {
	now := at(9, 0)
	h := &fakeHost{accounts: []router.AccountView{claude("a", five(0, nil, now))}, stamp: "v1",
		results: []Result{{Status: 401, Err: errors.New("unauthorized")}}}
	g := newTest(h, &now)
	g.Tick()
	g.wait()
	now = now.Add(time.Hour)
	g.Tick()
	g.wait()
	if h.firedCount() != 1 {
		t.Fatal("credential block must stop retries")
	}
	h.stamp = "v2"
	g.Tick()
	g.wait()
	if h.firedCount() != 2 {
		t.Fatalf("new credential must resume ignition, fired=%d", h.firedCount())
	}
}

func TestRateLimitPausesUntilNextDay(t *testing.T) {
	now := at(9, 0)
	h := &fakeHost{accounts: []router.AccountView{claude("a", five(0, nil, now))}, results: []Result{{Status: 429, Err: errors.New("rate limited")}}}
	g := newTest(h, &now)
	g.Tick()
	g.wait()
	now = at(21, 0)
	g.Tick()
	g.wait()
	if h.firedCount() != 1 {
		t.Fatal("429 must pause until the next day")
	}
	now = time.Date(2026, 11, 28, 7, 0, 1, 0, time.UTC)
	g.Tick()
	g.wait()
	if h.firedCount() != 2 {
		t.Fatal("expected ignition at next day start")
	}
}

func TestTransientBackoffThenPause(t *testing.T) {
	now := at(9, 0)
	fail := Result{Status: 502, Err: errors.New("bad gateway")}
	h := &fakeHost{accounts: []router.AccountView{claude("a", five(0, nil, now))}, results: []Result{fail, fail, fail}}
	g := newTest(h, &now)
	g.Tick()
	g.wait()
	now = now.Add(4 * time.Minute)
	g.Tick()
	g.wait()
	if h.firedCount() != 1 {
		t.Fatal("retry must wait 5 minutes")
	}
	now = now.Add(2 * time.Minute)
	g.Tick()
	g.wait()
	now = now.Add(16 * time.Minute)
	g.Tick()
	g.wait()
	if h.firedCount() != 3 {
		t.Fatalf("expected 3 attempts, got %d", h.firedCount())
	}
	now = now.Add(time.Hour)
	g.Tick()
	g.wait()
	if h.firedCount() != 3 || g.Events(1)[0].Kind != EventPaused {
		t.Fatalf("expected pause after 3 failures, events=%+v", g.Events(1))
	}
}

func TestModelRefusedFallsBack(t *testing.T) {
	now := at(9, 0)
	h := &fakeHost{accounts: []router.AccountView{claude("a", five(0, nil, now))}, results: []Result{{Status: 404, Err: errors.New("model not found")}}}
	g := newTest(h, &now)
	g.Tick()
	g.wait()
	if len(h.fired) != 2 || h.fired[1] != "claude-haiku-4-5" {
		t.Fatalf("expected fallback to older haiku, got %v", h.fired)
	}
}

func TestNoWindowStartedPauses(t *testing.T) {
	now := at(9, 0)
	h := &fakeHost{accounts: []router.AccountView{claude("a", five(0, nil, now))}}
	g := newTest(h, &now)
	g.Tick()
	g.wait()
	now = now.Add(5 * time.Minute)
	g.Tick()
	if ev := g.Events(1); ev[0].Kind != EventNoWindow {
		t.Fatalf("expected no_window_started, got %+v", ev)
	}
}

func TestDecode(t *testing.T) {
	cfg, err := Decode([]byte("mode: active\nignition:\n  enabled: true\n  start_hour: 6\n  providers:\n    codex:\n      enabled: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.StartHour != 6 || cfg.EndHour != 22 {
		t.Fatalf("unexpected %+v", cfg)
	}
	if _, ok := cfg.provider("codex"); ok {
		t.Fatal("codex disabled")
	}
	if _, ok := cfg.provider("claude"); !ok {
		t.Fatal("claude keeps default")
	}
	if _, err := Decode([]byte("ignition:\n  start_hour: 23\n  end_hour: 2\n")); err == nil {
		t.Fatal("expected validation error")
	}
}
