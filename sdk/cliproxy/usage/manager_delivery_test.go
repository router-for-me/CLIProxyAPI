package usage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
)

type deliveryPluginFunc func(context.Context, Record)

func (f deliveryPluginFunc) HandleUsage(ctx context.Context, record Record) { f(ctx, record) }

func joinDelivery(t *testing.T, m *Manager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatal("dispatcher did not finish")
	}
}

func awaitDeliverySignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("dispatcher did not reach the test barrier")
	}
}

func assertDeliveryAccounting(t *testing.T, s DeliveryStats) {
	t.Helper()
	if s.Accepted != s.Delivered+s.Failed+s.Unhandled+uint64(s.Queued+s.InFlight) {
		t.Fatal("accepted record accounting does not balance")
	}
	if s.Queued < 0 || s.Queued > s.Capacity || s.InFlight < 0 || s.InFlight > 1 {
		t.Fatal("queue or in-flight bound exceeded")
	}
}

func TestManagerHonorsBufferWithBlockedPlugin(t *testing.T) {
	m := NewManager(2)
	entered, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(release) }); joinDelivery(t, m) })
	var delivered []int64
	m.Register(deliveryPluginFunc(func(_ context.Context, r Record) {
		if r.Detail.InputTokens == 0 {
			close(entered)
			<-release
		}
		delivered = append(delivered, r.Detail.InputTokens)
	}))
	m.Publish(context.Background(), Record{}) // Publish also starts the dispatcher.
	awaitDeliverySignal(t, entered)
	for i := int64(1); i <= 8; i++ {
		m.Publish(context.Background(), Record{Detail: Detail{InputTokens: i}})
	}
	if m.TryPublish(context.Background(), Record{}) {
		t.Fatal("full queue accepted another record")
	}
	s := m.Stats()
	assertDeliveryAccounting(t, s)
	if s.Accepted != 3 || s.RejectedFull != 7 || s.Queued != 2 || s.InFlight != 1 {
		t.Fatal("saturation accounting differs from the known admission sequence")
	}
	unblock.Do(func() { close(release) })
	joinDelivery(t, m)
	if len(delivered) != 3 || delivered[0] != 0 || delivered[1] != 1 || delivered[2] != 2 {
		t.Fatal("accepted records were lost, duplicated or reordered")
	}
	s = m.Stats()
	assertDeliveryAccounting(t, s)
	if s.Delivered != 3 || !s.Closed || !s.Stopped || s.InFlight != 0 || s.Queued != 0 {
		t.Fatal("joined dispatcher did not finish all accepted records")
	}
}

func TestManagerShutdownTimeoutRetainsAcceptedRecords(t *testing.T) {
	m := NewManager(1)
	entered, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(release) }); joinDelivery(t, m) })
	m.Register(deliveryPluginFunc(func(_ context.Context, _ Record) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
	}))
	if !m.TryPublish(nil, Record{}) {
		t.Fatal("initial admission rejected")
	}
	awaitDeliverySignal(t, entered)
	if !m.TryPublish(nil, Record{}) {
		t.Fatal("buffered admission rejected")
	}
	deadline, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	for range 3 {
		if err := m.Shutdown(deadline); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("blocked dispatch incorrectly reported a completed shutdown")
		}
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if err := m.Shutdown(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal("shutdown did not return caller cancellation")
	}
	s := m.Stats()
	assertDeliveryAccounting(t, s)
	if !s.Closed || s.Stopped || s.Accepted != 2 || s.Delivered != 0 || s.Queued != 1 || s.InFlight != 1 {
		t.Fatal("timeout lost accepted work or claimed completed delivery")
	}
	if m.TryPublish(nil, Record{}) {
		t.Fatal("publication after shutdown was accepted")
	}
	const waiters = 16
	errs := make(chan error, waiters)
	ctx, finish := context.WithTimeout(context.Background(), 3*time.Second)
	defer finish()
	for range waiters {
		go func() { errs <- m.Shutdown(ctx) }()
	}
	unblock.Do(func() { close(release) })
	for range waiters {
		if <-errs != nil {
			t.Fatal("concurrent shutdown did not join the same worker")
		}
	}
	joinDelivery(t, m)
	s = m.Stats()
	assertDeliveryAccounting(t, s)
	if !s.Stopped || s.Delivered != 2 || s.RejectedClosed != 1 {
		t.Fatal("later shutdown did not retain timeout-era accounting")
	}
}

func TestManagerConcurrentPublishStartStopAndShutdown(t *testing.T) {
	m := NewManager(8)
	var mu sync.Mutex
	seen := make(map[string]bool)
	duplicate := false
	m.Register(deliveryPluginFunc(func(_ context.Context, r Record) {
		mu.Lock()
		defer mu.Unlock()
		if seen[r.RequestID] {
			duplicate = true
		}
		seen[r.RequestID] = true
	}))
	var accepted atomic.Uint64
	var admittedMu sync.Mutex
	admitted := make(map[string]bool)
	if !m.TryPublish(nil, Record{RequestID: "initial"}) {
		t.Fatal("initial concurrent test admission rejected")
	}
	accepted.Add(1)
	admitted["initial"] = true
	start := make(chan struct{})
	var workers sync.WaitGroup
	for publisher := range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for item := range 64 {
				id := fmt.Sprintf("%d/%d", publisher, item)
				m.Start(context.Background())
				if m.TryPublish(nil, Record{RequestID: id}) {
					accepted.Add(1)
					admittedMu.Lock()
					admitted[id] = true
					admittedMu.Unlock()
				}
				assertDeliveryAccounting(t, m.Stats())
			}
		}()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	errs := make(chan error, 8)
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			m.Stop()
			errs <- m.Shutdown(ctx)
		}()
	}
	close(start)
	workers.Wait()
	for range 8 {
		if <-errs != nil {
			t.Fatal("concurrent shutdown failed")
		}
	}
	joinDelivery(t, m)
	s := m.Stats()
	assertDeliveryAccounting(t, s)
	if s.Accepted+s.RejectedFull+s.RejectedClosed != 16*64+1 || s.Accepted != accepted.Load() || s.Delivered != s.Accepted || !s.Stopped {
		t.Fatal("concurrent admission or delivery counts are inconsistent")
	}
	if duplicate || len(seen) != len(admitted) {
		t.Fatal("concurrent delivery lost or duplicated accepted records")
	}
	for id := range admitted {
		if !seen[id] {
			t.Fatal("an accepted record was never delivered")
		}
	}
}

func TestManagerPanicIsCountedAndLaterPluginsStillRun(t *testing.T) {
	m := NewManager(4)
	var output bytes.Buffer
	previous := log.StandardLogger().Out
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	for range 2 {
		m.Register(deliveryPluginFunc(func(_ context.Context, r Record) {
			if r.Detail.InputTokens == 1 {
				panic("private-panic-canary")
			}
		}))
	}
	var received int
	m.Register(deliveryPluginFunc(func(context.Context, Record) { received++ }))
	for i := int64(1); i <= 2; i++ {
		if !m.TryPublish(nil, Record{Detail: Detail{InputTokens: i}}) {
			t.Fatal("panic test admission rejected")
		}
	}
	joinDelivery(t, m)
	s := m.Stats()
	assertDeliveryAccounting(t, s)
	if received != 2 || s.Accepted != 2 || s.Failed != 1 || s.PluginPanics != 2 || s.Delivered != 1 || s.Unhandled != 0 {
		t.Fatal("panic counts or continued delivery differ from expected callback outcomes")
	}
	if strings.Contains(output.String(), "private-panic-canary") {
		t.Fatal("panic value was written to the log")
	}
}

func TestManagerEmptyAndRepeatedLifecycle(t *testing.T) {
	for _, mode := range []string{"new", "zero", "negative", "zero-value", "started"} {
		t.Run(mode, func(t *testing.T) {
			m := NewManager(2)
			switch mode {
			case "zero":
				m = NewManager(0)
			case "negative":
				m = NewManager(-1)
			case "zero-value":
				m = &Manager{}
			case "started":
				m.Start(nil)
			}
			joinDelivery(t, m) // Includes Shutdown before Start.
			m.Stop()
			m.Start(context.Background())
			if m.TryPublish(nil, Record{}) {
				t.Fatal("Start reopened a stopped manager")
			}
			joinDelivery(t, m)
			s := m.Stats()
			assertDeliveryAccounting(t, s)
			capacity := 2
			if mode == "zero" || mode == "negative" || mode == "zero-value" {
				capacity = DefaultQueueCapacity
			}
			if !s.Stopped || !s.Closed || s.Accepted != 0 || s.RejectedClosed != 1 || s.Capacity != capacity {
				t.Fatal("empty lifecycle did not preserve its closed state and capacity")
			}
		})
	}
	var missing *Manager
	missing.Start(nil)
	missing.Stop()
	missing.Publish(nil, Record{})
	if missing.TryPublish(nil, Record{}) || missing.Shutdown(nil) != nil || missing.Stats() != (DeliveryStats{}) {
		t.Fatal("nil manager compatibility changed")
	}
}

func TestManagerCountsRecordsWithoutPlugins(t *testing.T) {
	m := NewManager(1)
	if !m.TryPublish(nil, Record{}) {
		t.Fatal("admission without a plugin rejected")
	}
	joinDelivery(t, m)
	s := m.Stats()
	assertDeliveryAccounting(t, s)
	if s.Accepted != 1 || s.Unhandled != 1 || s.Delivered != 0 || s.Failed != 0 {
		t.Fatal("record without a plugin was reported as delivered")
	}
}

func TestManagerRingWrapsAndPreservesCancelledContext(t *testing.T) {
	m := NewManager(2)
	defer joinDelivery(t, m)
	observed := make(chan int64)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.Register(deliveryPluginFunc(func(ctx context.Context, r Record) {
		if !errors.Is(ctx.Err(), context.Canceled) {
			observed <- -1
			return
		}
		observed <- r.Detail.InputTokens
	}))
	for i := int64(0); i < 20; i++ {
		if !m.TryPublish(ctx, Record{Detail: Detail{InputTokens: i}}) {
			t.Fatal("empty ring rejected admission")
		}
		select {
		case got := <-observed:
			if got != i {
				t.Fatal("ring order or original context was not preserved")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("ring did not dispatch accepted record")
		}
	}
	joinDelivery(t, m)
	s := m.Stats()
	assertDeliveryAccounting(t, s)
	if s.Delivered != 20 || s.RejectedFull != 0 {
		t.Fatal("ring wrapping lost records")
	}
}
