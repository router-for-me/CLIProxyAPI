package usage

import (
	"context"
	"testing"
	"time"
)

type pluginFunc func(context.Context, Record)

func (f pluginFunc) HandleUsage(ctx context.Context, record Record) {
	f(ctx, record)
}

func TestManagerFlushWaitsForQueueAndInFlightRecord(t *testing.T) {
	manager := NewManager(8)
	started := make(chan struct{})
	release := make(chan struct{})
	blocked := true

	manager.Register(pluginFunc(func(_ context.Context, _ Record) {
		if blocked {
			blocked = false
			close(started)
			<-release
		}
	}))

	manager.Publish(context.Background(), Record{Provider: "first"})
	<-started
	manager.Publish(context.Background(), Record{Provider: "second"})

	flushDone := make(chan error, 1)
	go func() {
		flushDone <- manager.Flush(context.Background())
	}()

	select {
	case err := <-flushDone:
		t.Fatalf("Flush returned before queued record completed: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	close(release)

	select {
	case err := <-flushDone:
		if err != nil {
			t.Fatalf("Flush returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Flush did not return after queued work completed")
	}
}

func TestManagerDropsOldestQueuedRecordWhenFull(t *testing.T) {
	manager := NewManager(2)
	started := make(chan struct{})
	release := make(chan struct{})
	seen := make(chan string, 4)

	manager.Register(pluginFunc(func(_ context.Context, record Record) {
		seen <- record.Provider
		if record.Provider == "first" {
			close(started)
			<-release
		}
	}))

	manager.Publish(context.Background(), Record{Provider: "first"})
	<-started

	manager.Publish(context.Background(), Record{Provider: "second"})
	manager.Publish(context.Background(), Record{Provider: "third"})
	manager.Publish(context.Background(), Record{Provider: "fourth"})
	close(release)

	if err := manager.Flush(context.Background()); err != nil {
		t.Fatalf("Flush returned error: %v", err)
	}
	close(seen)

	var records []string
	for record := range seen {
		records = append(records, record)
	}
	want := []string{"first", "third", "fourth"}
	if len(records) != len(want) {
		t.Fatalf("records = %v, want %v", records, want)
	}
	for i := range want {
		if records[i] != want[i] {
			t.Fatalf("records = %v, want %v", records, want)
		}
	}
}
