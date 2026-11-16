package cliproxy

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

var quotaHistoryRecorderNow = time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)

func claudeWeeklyQuotaAuth(id string, observedAt time.Time, used float64) *coreauth.Auth {
	return &coreauth.Auth{
		ID:       id,
		Provider: "claude",
		Quota: coreauth.QuotaState{
			ObservedAt: observedAt,
			Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-7d-Utilization": strconv.FormatFloat(used, 'f', -1, 64),
				"Anthropic-Ratelimit-Unified-7d-Reset":       strconv.FormatInt(quotaHistoryRecorderNow.Add(3*24*time.Hour).Unix(), 10),
			},
		},
	}
}

type quotaHistoryRecorderFixture struct {
	path     string
	store    *coreauth.QuotaHistory
	recorder *quotaHistoryRecorder
	clock    time.Time
	auths    []*coreauth.Auth
}

func newQuotaHistoryRecorderFixture(t *testing.T) *quotaHistoryRecorderFixture {
	t.Helper()
	f := &quotaHistoryRecorderFixture{path: filepath.Join(t.TempDir(), coreauth.QuotaHistoryFileName), clock: quotaHistoryRecorderNow}
	store, errOpen := coreauth.OpenQuotaHistory(f.path, f.clock)
	if errOpen != nil {
		t.Fatalf("OpenQuotaHistory: %v", errOpen)
	}
	t.Cleanup(func() {
		if errClose := store.Close(); errClose != nil {
			t.Errorf("Close: %v", errClose)
		}
	})
	f.store = store
	f.recorder = &quotaHistoryRecorder{
		store:       store,
		list:        func() []*coreauth.Auth { return f.auths },
		now:         func() time.Time { return f.clock },
		lastCompact: f.clock,
	}
	return f
}

func (f *quotaHistoryRecorderFixture) tickAt(at time.Time, used float64) {
	f.clock = at
	f.auths = []*coreauth.Auth{claudeWeeklyQuotaAuth("claude-a.json", at, used)}
	f.recorder.tick()
}

func countQuotaHistoryLines(t *testing.T, path string) int {
	t.Helper()
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read history file: %v", errRead)
	}
	return bytes.Count(data, []byte("\n"))
}

func statQuotaHistory(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, errStat := os.Stat(path)
	if errStat != nil {
		t.Fatalf("stat history file: %v", errStat)
	}
	return info
}

func TestQuotaHistoryRecorderTickObservesList(t *testing.T) {
	f := newQuotaHistoryRecorderFixture(t)
	f.auths = []*coreauth.Auth{
		claudeWeeklyQuotaAuth("claude-a.json", f.clock.Add(-time.Minute), 0.25),
		{ID: "no-quota.json", Provider: "claude"},
	}
	f.recorder.tick()
	f.clock = f.clock.Add(time.Minute)
	f.recorder.tick()

	series := f.store.Series("claude-a.json", time.Time{})
	if len(series) != 1 || len(series[0].Points) != 1 || series[0].Points[0].Used != 0.25 {
		t.Fatalf("series = %+v, want one reading of 0.25", series)
	}
	if other := f.store.Series("no-quota.json", time.Time{}); other != nil {
		t.Fatalf("credential without quota recorded %+v", other)
	}
	if lines := countQuotaHistoryLines(t, f.path); lines != 1 {
		t.Fatalf("history file holds %d lines, want 1", lines)
	}
}

func TestQuotaHistoryRecorderCompactsDaily(t *testing.T) {
	f := newQuotaHistoryRecorderFixture(t)
	opened := f.clock
	f.tickAt(opened, 0.1)
	first := statQuotaHistory(t, f.path)

	f.tickAt(opened.Add(time.Minute), 0.2)
	if !os.SameFile(first, statQuotaHistory(t, f.path)) || !f.recorder.lastCompact.Equal(opened) {
		t.Fatal("the first ticks after Open compacted the file")
	}

	f.tickAt(opened.Add(24*time.Hour), 0.3)
	if os.SameFile(first, statQuotaHistory(t, f.path)) || !f.recorder.lastCompact.Equal(opened.Add(24*time.Hour)) {
		t.Fatal("a tick 24h after the last compaction did not compact")
	}
	if lines := countQuotaHistoryLines(t, f.path); lines != 3 {
		t.Fatalf("compacted file holds %d lines, want 3", lines)
	}
}

func TestQuotaHistoryRecorderCompactsNextTickWhenDirty(t *testing.T) {
	f := newQuotaHistoryRecorderFixture(t)
	// A directory where the file should be makes both the append and the
	// rewrite fail, even for root.
	if errMkdir := os.Mkdir(f.path, 0o700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	f.tickAt(f.clock, 0.1)
	if !f.store.Dirty() {
		t.Fatal("a failed write did not mark the store dirty")
	}

	if errRemove := os.Remove(f.path); errRemove != nil {
		t.Fatal(errRemove)
	}
	next := f.clock.Add(time.Minute)
	f.tickAt(next, 0.1)
	if f.store.Dirty() || !f.recorder.lastCompact.Equal(next) {
		t.Fatalf("dirty=%v lastCompact=%s, want the next tick to compact", f.store.Dirty(), f.recorder.lastCompact)
	}
	if lines := countQuotaHistoryLines(t, f.path); lines != 1 {
		t.Fatalf("healed file holds %d lines, want 1", lines)
	}
}

func TestQuotaHistoryRecorderRunRecordsAndUnpublishes(t *testing.T) {
	t.Cleanup(func() { coreauth.SetActiveQuotaHistory(nil) })
	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(context.Background(), claudeWeeklyQuotaAuth("claude-a.json", time.Now().Add(-time.Minute), 0.5)); errRegister != nil {
		t.Fatalf("Register: %v", errRegister)
	}
	s := &Service{cfg: &config.Config{AuthDir: authDir}, coreManager: manager}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s.runQuotaHistoryRecorder(ctx)

	if coreauth.ActiveQuotaHistory() != nil {
		t.Fatal("the store stays published after the recorder stopped")
	}
	if lines := countQuotaHistoryLines(t, filepath.Join(authDir, coreauth.QuotaHistoryFileName)); lines != 1 {
		t.Fatalf("history file holds %d lines, want 1", lines)
	}
}
