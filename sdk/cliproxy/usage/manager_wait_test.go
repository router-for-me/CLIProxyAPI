package usage

import (
	"context"
	"sync/atomic"
	"testing"
)

// gatedPlugin blocks its first delivery until released, so records pile up
// in the queue behind it.
type gatedPlugin struct {
	gate      chan struct{}
	entered   chan struct{}
	delivered atomic.Int64
}

func (p *gatedPlugin) HandleUsage(context.Context, Record) {
	if p.delivered.Add(1) == 1 {
		close(p.entered)
		<-p.gate
	}
}

func TestManagerWaitDeliversEverythingQueuedBeforeStop(t *testing.T) {
	const records = 20
	mgr := NewManager(0)
	plugin := &gatedPlugin{gate: make(chan struct{}), entered: make(chan struct{})}
	mgr.Register(plugin)
	for range records {
		mgr.Publish(context.Background(), Record{Model: "m"})
	}
	<-plugin.entered
	mgr.Stop()

	// The dispatcher is still inside the first delivery, so a wait that has
	// already run out of time reports the queue as not drained.
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	if mgr.Wait(expired) {
		t.Fatal("Wait reported a drained queue while a delivery was blocked")
	}

	close(plugin.gate)
	if !mgr.Wait(context.Background()) {
		t.Fatal("Wait did not report the queue as drained")
	}
	if got := plugin.delivered.Load(); got != records {
		t.Fatalf("delivered = %d, want all %d records queued before Stop", got, records)
	}
}

func TestManagerWaitReturnsAtOnceWhenNeverStarted(t *testing.T) {
	if !NewManager(0).Wait(context.Background()) {
		t.Fatal("Wait blocked on a dispatcher that never started")
	}
}

func TestManagerWaitReportsDrainedQueueAfterDeadline(t *testing.T) {
	mgr := NewManager(0)
	mgr.Start(context.Background())
	mgr.Stop()
	if !mgr.Wait(context.Background()) {
		t.Fatal("Wait did not report the queue as drained")
	}

	// The queue is already drained, so an expired deadline must not turn the
	// answer into "not drained".
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	for range 100 {
		if !mgr.Wait(expired) {
			t.Fatal("Wait reported a drained queue as not drained after its deadline")
		}
	}
}
