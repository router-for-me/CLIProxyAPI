package usage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func openTestOutbox(t *testing.T, cfg config.AccountingOutboxConfig) *Outbox {
	t.Helper()
	if cfg.DataPath == "" {
		cfg.DataPath = t.TempDir()
	}
	o, err := OpenOutbox(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := o.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return o
}

func testEvent(id string) AccountingEvent {
	return AccountingEvent{SchemaVersion: AccountingEventSchemaVersion, ExecutionID: id, CompletedAt: time.Unix(100, 0)}
}

func TestOutboxRecoveryDuplicateAndOwnership(t *testing.T) {
	cfg := config.AccountingOutboxConfig{DataPath: t.TempDir()}
	o := openTestOutbox(t, cfg)
	event := testEvent("a")
	if err := o.Insert(event); err != nil {
		t.Fatal(err)
	}
	event.Provider = "changed"
	if err := o.Insert(event); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOutbox(cfg); err == nil {
		t.Fatal("second owner accepted")
	}
	claimed, err := o.Claim("a")
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	if err := o.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	recovered := openTestOutbox(t, cfg)
	raw, err := recovered.Event("a")
	if err != nil || raw.Provider != "" {
		t.Fatalf("raw changed: %+v %v", raw, err)
	}
	delivery, err := recovered.Delivery("a")
	if err != nil || delivery.State != DeliveryAmbiguous || delivery.Attempts != 1 {
		t.Fatalf("recovery: %+v %v", delivery, err)
	}
	if claimed, err := recovered.Claim("a"); err != nil || claimed {
		t.Fatalf("ambiguous retry: %v %v", claimed, err)
	}
}

func TestOutboxStatesRetentionAndCapacity(t *testing.T) {
	o := openTestOutbox(t, config.AccountingOutboxConfig{MaxEvents: 5, RetentionHours: 1})
	now := time.Unix(10000, 0)
	o.now = func() time.Time { return now }
	states := []DeliveryState{DeliveryPending, DeliveryAcknowledged, DeliveryRetryable, DeliveryAmbiguous, DeliveryRejected}
	for _, state := range states {
		id := string(state)
		if err := o.Insert(testEvent(id)); err != nil {
			t.Fatal(err)
		}
		if state != DeliveryPending {
			if claimed, err := o.Claim(id); err != nil || !claimed {
				t.Fatal(err)
			}
			if err := o.Resolve(id, 1, state, now.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := o.Insert(testEvent("overflow")); !errors.Is(err, ErrOutboxFull) {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if err := o.Prune(); err != nil {
		t.Fatal(err)
	}
	for _, state := range states {
		_, err := o.Event(string(state))
		terminal := state == DeliveryAcknowledged || state == DeliveryRejected
		if terminal != errors.Is(err, os.ErrNotExist) {
			t.Fatalf("retention %s: %v", state, err)
		}
	}
	stats, err := o.Stats()
	if err != nil || stats.Backlog != 3 || stats.OldestPendingAgeSeconds != 17100 {
		t.Fatalf("stats %+v %v", stats, err)
	}
	if err := o.Insert(testEvent("after-retention")); err != nil {
		t.Fatal(err)
	}
}

func TestOutboxConcurrentClaim(t *testing.T) {
	o := openTestOutbox(t, config.AccountingOutboxConfig{})
	if err := o.Insert(testEvent("a")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	claimed := make(chan bool, 2)
	for range 2 {
		wg.Go(func() {
			ok, err := o.Claim("a")
			if err != nil {
				t.Error(err)
			}
			claimed <- ok
		})
	}
	wg.Wait()
	if <-claimed == <-claimed {
		t.Fatal("expected exactly one owner")
	}
}

func TestOutboxVolatileWindowPressureAndShutdown(t *testing.T) {
	// A constructed, unstarted sink makes the pre-commit window deterministic.
	o := &Outbox{queue: make(chan []byte, 1), done: make(chan struct{}), closeDone: make(chan struct{})}
	o.HandleAccountingEvent(testEvent("queued"))
	o.HandleAccountingEvent(testEvent("dropped"))
	if len(o.queue) != 1 || o.dropped.Load() != 1 {
		t.Fatal("queue not bounded")
	}
	o.closed = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := o.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	o.HandleAccountingEvent(testEvent("closed"))
	if o.dropped.Load() != 2 {
		t.Fatal("closed admission not counted")
	}
	// No insertion has happened. Reopening an independent store recovers no queued event.
	recovered := openTestOutbox(t, config.AccountingOutboxConfig{})
	if _, err := recovered.Event("queued"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestOutboxStorageFailures(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "accounting.db"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOutbox(config.AccountingOutboxConfig{DataPath: dir}); err == nil {
		t.Fatal("corruption accepted")
	}

	o := openTestOutbox(t, config.AccountingOutboxConfig{MaxDiskMB: 8})
	if err := os.Truncate(o.db.Path(), 8*1024*1024); err != nil {
		t.Fatal(err)
	}
	if err := o.Insert(testEvent("full")); !errors.Is(err, ErrOutboxFull) {
		t.Fatalf("disk budget: %v", err)
	}
	o.HandleAccountingEvent(testEvent("async-full"))
	if err := o.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if o.failures.Load() != 1 || o.dropped.Load() != 1 {
		t.Fatalf("failure counters: %d %d", o.failures.Load(), o.dropped.Load())
	}
}

type blockingUsagePlugin struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *blockingUsagePlugin) HandleUsage(context.Context, Record) {
	p.once.Do(func() { close(p.started); <-p.release })
}

func TestAccountingIngressIndependentOfDispatcher(t *testing.T) {
	cfg := config.AccountingOutboxConfig{DataPath: t.TempDir()}
	o := openTestOutbox(t, cfg)
	m := NewManager(1)
	blocker := &blockingUsagePlugin{started: make(chan struct{}), release: make(chan struct{})}
	m.Register(blocker)
	m.Publish(context.Background(), Record{RequestID: "block-dispatch"})
	<-blocker.started
	defer close(blocker.release)
	defer m.Stop()
	detach, err := m.AttachOutbox(o)
	if err != nil {
		t.Fatal(err)
	}
	defer detach()
	if _, err := m.AttachOutbox(o); err == nil {
		t.Fatal("duplicate attachment")
	}
	m.Publish(context.Background(), Record{RequestID: "before-dispatch"})
	if err := o.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered := openTestOutbox(t, cfg)
	if _, err := recovered.Event("before-dispatch"); err != nil {
		t.Fatal(err)
	}
	detach()
	detach()
	detachNext, err := m.AttachOutbox(recovered)
	if err != nil {
		t.Fatal(err)
	}
	detachNext()
}

func TestOutboxShutdownWhileStorageBlocked(t *testing.T) {
	cfg := config.AccountingOutboxConfig{DataPath: t.TempDir()}
	o := openTestOutbox(t, cfg)
	tx, err := o.db.Begin(true)
	if err != nil {
		t.Fatal(err)
	}
	o.HandleAccountingEvent(testEvent("queued"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := o.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := o.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered := openTestOutbox(t, cfg)
	if _, err := recovered.Event("queued"); err != nil {
		t.Fatal(err)
	}
}

func TestOutboxDiskCeiling(t *testing.T) {
	o := openTestOutbox(t, config.AccountingOutboxConfig{MaxDiskMB: 8})
	event := testEvent("")
	event.RequestedAlias = strings.Repeat("x", 10000)
	for i := 0; i < 10000; i++ {
		event.ExecutionID = strconv.Itoa(i)
		err := o.Insert(event)
		if errors.Is(err, ErrOutboxFull) {
			info, err := os.Stat(o.db.Path())
			if err != nil || info.Size() > 8*1024*1024 {
				t.Fatalf("disk ceiling: %v %v", info, err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("disk budget never enforced")
}

func TestOutboxRetryAndStaleResolution(t *testing.T) {
	o := openTestOutbox(t, config.AccountingOutboxConfig{})
	now := time.Unix(10000, 0)
	o.now = func() time.Time { return now }
	if err := o.Insert(testEvent("a")); err != nil {
		t.Fatal(err)
	}
	if claimed, err := o.Claim("a"); err != nil || !claimed {
		t.Fatal(err)
	}
	if err := o.Resolve("a", 1, DeliveryRetryable, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if claimed, err := o.Claim("a"); err != nil || claimed {
		t.Fatal("early retry")
	}
	now = now.Add(time.Hour)
	if claimed, err := o.Claim("a"); err != nil || !claimed {
		t.Fatal(err)
	}
	if err := o.Resolve("a", 1, DeliveryAcknowledged, time.Time{}); err == nil {
		t.Fatal("stale resolution accepted")
	}
	if err := o.Resolve("a", 2, DeliveryAcknowledged, time.Time{}); err != nil {
		t.Fatal(err)
	}
}
