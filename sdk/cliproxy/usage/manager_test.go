package usage

import (
	"context"
	"sync"
	"testing"
	"time"
)

type recordingPlugin struct {
	mu      sync.Mutex
	records []Record
	release chan struct{}
	started chan struct{}
}

func (p *recordingPlugin) HandleUsage(ctx context.Context, record Record) {
	if p.started != nil {
		select {
		case <-p.started:
		default:
			close(p.started)
		}
	}
	if p.release != nil {
		<-p.release
	}
	p.mu.Lock()
	p.records = append(p.records, record)
	p.mu.Unlock()
}

func TestManagerStopDrainsQueueBeforeReturn(t *testing.T) {
	m := NewManager(4)
	plugin := &recordingPlugin{
		release: make(chan struct{}),
		started: make(chan struct{}),
	}
	m.Register(plugin)

	m.Publish(context.Background(), Record{Provider: "gemini", Model: "m1"})
	m.Publish(context.Background(), Record{Provider: "gemini", Model: "m2"})

	<-plugin.started

	stopDone := make(chan struct{})
	go func() {
		m.Stop()
		close(stopDone)
	}()

	select {
	case <-stopDone:
		t.Fatal("Stop returned before queued records were drained")
	case <-time.After(50 * time.Millisecond):
	}

	close(plugin.release)

	select {
	case <-stopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return after draining queued records")
	}

	plugin.mu.Lock()
	defer plugin.mu.Unlock()
	if len(plugin.records) != 2 {
		t.Fatalf("records processed = %d, want 2", len(plugin.records))
	}
}
