package usagestate

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/usageaggregate"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type fakeCounters struct {
	records []coreauth.AuthRequestCounts
	rev     uint64
	applied []coreauth.AuthRequestCounts
}

func (f *fakeCounters) RequestCountersRevision() uint64 { return f.rev }

func (f *fakeCounters) RequestCountersSnapshot() []coreauth.AuthRequestCounts { return f.records }

func (f *fakeCounters) ApplyRequestCounters(records []coreauth.AuthRequestCounts) int {
	f.applied = records
	f.records = records
	f.rev++
	return len(records)
}

type fakeTokens struct {
	snapshot usageaggregate.UsageSnapshot
	rev      uint64
	restored bool
}

func (f *fakeTokens) Revision() uint64 { return f.rev }

func (f *fakeTokens) Snapshot() usageaggregate.UsageSnapshot { return f.snapshot }

func (f *fakeTokens) Restore(snapshot usageaggregate.UsageSnapshot) {
	f.snapshot = snapshot
	f.restored = true
	f.rev++
}

func TestFlushSkipsWhenNothingChanged(t *testing.T) {
	dir := t.TempDir()
	path := UsageStatePath(dir)
	manager := New(path, DefaultInterval, &fakeCounters{}, &fakeTokens{})

	if errLoad := manager.Load(); errLoad != nil {
		t.Fatalf("load missing file: %v", errLoad)
	}
	if errFlush := manager.Flush(context.Background()); errFlush != nil {
		t.Fatalf("flush: %v", errFlush)
	}
	if _, errStat := os.Stat(path); !os.IsNotExist(errStat) {
		t.Fatalf("expected no file written when nothing changed, stat err = %v", errStat)
	}
}

func TestFlushWritesSingleFileAndLoadRestores(t *testing.T) {
	dir := t.TempDir()
	path := UsageStatePath(dir)

	counters := &fakeCounters{
		records: []coreauth.AuthRequestCounts{
			{Provider: "codex", AuthID: "auth-1", AuthIndex: "idx-1", Success: 7, Failed: 2},
		},
		rev: 1,
	}
	tokens := &fakeTokens{
		snapshot: usageaggregate.UsageSnapshot{Requests: 9},
		rev:      1,
	}
	manager := New(path, DefaultInterval, counters, tokens)
	if errFlush := manager.Flush(context.Background()); errFlush != nil {
		t.Fatalf("flush: %v", errFlush)
	}

	if want := filepath.Join(dir, "usage", FileName); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if _, errStat := os.Stat(path); errStat != nil {
		t.Fatalf("state file not written: %v", errStat)
	}

	// Reload into fresh sources.
	restoredCounters := &fakeCounters{}
	restoredTokens := &fakeTokens{}
	reloaded := New(path, DefaultInterval, restoredCounters, restoredTokens)
	if errLoad := reloaded.Load(); errLoad != nil {
		t.Fatalf("reload: %v", errLoad)
	}
	if len(restoredCounters.applied) != 1 || restoredCounters.applied[0].Success != 7 {
		t.Fatalf("counters not restored: %+v", restoredCounters.applied)
	}
	if !restoredTokens.restored || restoredTokens.snapshot.Requests != 9 {
		t.Fatalf("tokens not restored: %+v", restoredTokens.snapshot)
	}
}
