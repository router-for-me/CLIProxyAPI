package usage

import (
	"context"
	"sync"
	"testing"
	"time"
)

type blockingPlugin struct {
	started  chan struct{}
	release  chan struct{}
	released chan struct{}
	once     sync.Once
}

func (p *blockingPlugin) HandleUsage(ctx context.Context, record Record) {
	p.once.Do(func() {
		close(p.started)
	})
	<-p.release
	close(p.released)
}

func TestStopWaitsForBlockedDispatchToDrain(t *testing.T) {
	manager := NewManager(1)
	plugin := &blockingPlugin{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		released: make(chan struct{}),
	}
	manager.Register(plugin)
	manager.Start(context.Background())
	manager.Publish(context.Background(), Record{Provider: "test"})

	select {
	case <-plugin.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for usage handler to start")
	}

	stopped := make(chan struct{})
	returnedBeforeRelease := make(chan struct{})
	go func() {
		manager.Stop()
		select {
		case <-plugin.released:
		default:
			close(returnedBeforeRelease)
		}
		close(stopped)
	}()

	manager.mu.Lock()
	for !manager.closed {
		manager.cond.Wait()
	}
	manager.mu.Unlock()

	select {
	case <-returnedBeforeRelease:
		t.Fatal("Stop returned before blocked usage handler was released")
	case <-time.After(50 * time.Millisecond):
	}

	close(plugin.release)

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Stop to return after usage handler was released")
	}
}
