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
