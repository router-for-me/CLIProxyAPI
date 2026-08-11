package usage

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestGenerateEnabledDefaultsNilToTrue(t *testing.T) {
	if !GenerateEnabled(nil) {
		t.Fatalf("GenerateEnabled(nil) = false, want true")
	}
}

func TestGenerateEnabledHonorsExplicitFalse(t *testing.T) {
	if GenerateEnabled(GenerateFlag(false)) {
		t.Fatalf("GenerateEnabled(false) = true, want false")
	}
}

func TestGenerateEnabledHonorsExplicitTrue(t *testing.T) {
	if !GenerateEnabled(GenerateFlag(true)) {
		t.Fatalf("GenerateEnabled(true) = false, want true")
	}
}

func TestGenerateFromContextDefaultsMissingToTrue(t *testing.T) {
	if !GenerateFromContext(context.Background()) {
		t.Fatalf("GenerateFromContext(background) = false, want true")
	}
}

func TestGenerateFromContextHonorsExplicitFalse(t *testing.T) {
	ctx := WithGenerate(context.Background(), false)
	if GenerateFromContext(ctx) {
		t.Fatalf("GenerateFromContext(false) = true, want false")
	}
}

func TestRecordOmittedGenerateIsEnabled(t *testing.T) {
	// Existing callers construct Record without setting Generate.
	// Omission must remain distinguishable from explicit false and default to true.
	record := Record{
		Provider: "openai",
		Model:    "gpt-5.4",
	}
	if record.Generate != nil {
		t.Fatalf("Record.Generate = %v, want nil for omitted field", record.Generate)
	}
	if !GenerateEnabled(record.Generate) {
		t.Fatalf("GenerateEnabled(omitted) = false, want true")
	}
}

type testPluginFunc func(context.Context, Record)

func (fn testPluginFunc) HandleUsage(ctx context.Context, record Record) {
	fn(ctx, record)
}

func TestManagerStopAndWaitDrainsQueuedRecords(t *testing.T) {
	manager := NewManager(4)
	handled := make(chan Record, 1)
	manager.Register(testPluginFunc(func(_ context.Context, record Record) {
		handled <- record
	}))
	manager.Publish(context.Background(), Record{Model: "test-model"})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.StopAndWait(ctx); err != nil {
		t.Fatalf("StopAndWait() error = %v", err)
	}

	select {
	case record := <-handled:
		if record.Model != "test-model" {
			t.Fatalf("handled model = %q, want test-model", record.Model)
		}
	default:
		t.Fatal("StopAndWait() returned before queued record was delivered")
	}
}

func TestManagerWaitHonorsContextWhilePluginIsBlocked(t *testing.T) {
	manager := NewManager(1)
	started := make(chan struct{})
	release := make(chan struct{})
	manager.Register(testPluginFunc(func(_ context.Context, _ Record) {
		close(started)
		<-release
	}))
	manager.Publish(context.Background(), Record{Model: "blocked"})

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("usage plugin did not start")
	}
	manager.Stop()

	timeoutCtx, cancelTimeout := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := manager.Wait(timeoutCtx)
	cancelTimeout()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait() error = %v, want context deadline exceeded", err)
	}

	close(release)
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), time.Second)
	defer cancelDrain()
	if err = manager.Wait(drainCtx); err != nil {
		t.Fatalf("Wait() after releasing plugin error = %v", err)
	}
}

func TestManagerStopAndWaitBeforeStart(t *testing.T) {
	manager := NewManager(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.StopAndWait(ctx); err != nil {
		t.Fatalf("StopAndWait() before Start error = %v", err)
	}
}

func TestManagerBoundsAsyncQueueWithoutDroppingCriticalAccounting(t *testing.T) {
	manager := NewManager(1)
	var criticalCount atomic.Int64
	var asyncCount atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})
	manager.RegisterCriticalNamed("accounting", testPluginFunc(func(_ context.Context, _ Record) {
		criticalCount.Add(1)
	}))
	manager.Register(testPluginFunc(func(_ context.Context, _ Record) {
		if asyncCount.Add(1) == 1 {
			close(started)
		}
		<-release
	}))

	manager.Publish(context.Background(), Record{Model: "first"})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("asynchronous plugin did not start")
	}
	manager.Publish(context.Background(), Record{Model: "queued"})
	manager.Publish(context.Background(), Record{Model: "dropped"})

	if got := manager.DroppedRecords(); got != 1 {
		t.Fatalf("DroppedRecords() = %d, want 1", got)
	}
	if got := criticalCount.Load(); got != 3 {
		t.Fatalf("critical deliveries = %d, want 3", got)
	}

	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.StopAndWait(ctx); err != nil {
		t.Fatalf("StopAndWait() error = %v", err)
	}
	if got := asyncCount.Load(); got != 2 {
		t.Fatalf("asynchronous deliveries = %d, want 2", got)
	}
}

func TestManagerStopAndWaitWaitsForInFlightCriticalPlugin(t *testing.T) {
	manager := NewManager(1)
	started := make(chan struct{})
	release := make(chan struct{})
	publishDone := make(chan struct{})
	manager.RegisterCriticalNamed("accounting", testPluginFunc(func(_ context.Context, _ Record) {
		close(started)
		<-release
	}))
	go func() {
		manager.Publish(context.Background(), Record{Model: "critical"})
		close(publishDone)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("critical plugin did not start")
	}

	stopDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		stopDone <- manager.StopAndWait(ctx)
	}()
	select {
	case err := <-stopDone:
		t.Fatalf("StopAndWait() returned before critical release: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	select {
	case <-publishDone:
	case <-time.After(time.Second):
		t.Fatal("Publish did not finish after critical release")
	}
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("StopAndWait() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("StopAndWait did not finish after critical release")
	}
}

func TestManagerStopDrainsAsyncDeliveryAdmittedBeforeStop(t *testing.T) {
	manager := NewManager(1)
	criticalStarted := make(chan struct{})
	criticalRelease := make(chan struct{})
	asyncHandled := make(chan Record, 1)
	publishDone := make(chan struct{})
	manager.RegisterCriticalNamed("accounting", testPluginFunc(func(_ context.Context, _ Record) {
		close(criticalStarted)
		<-criticalRelease
	}))
	manager.Register(testPluginFunc(func(_ context.Context, record Record) {
		asyncHandled <- record
	}))

	go func() {
		manager.Publish(context.Background(), Record{Model: "admitted"})
		close(publishDone)
	}()
	select {
	case <-criticalStarted:
	case <-time.After(time.Second):
		t.Fatal("critical plugin did not start")
	}
	manager.Stop()
	close(criticalRelease)

	select {
	case <-publishDone:
	case <-time.After(time.Second):
		t.Fatal("Publish did not finish")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Wait(ctx); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	select {
	case record := <-asyncHandled:
		if record.Model != "admitted" {
			t.Fatalf("handled model = %q, want admitted", record.Model)
		}
	default:
		t.Fatal("admitted record was not delivered to the asynchronous plugin")
	}
}

func TestManagerContextRoundTrip(t *testing.T) {
	manager := NewManager(1)
	ctx := WithManager(context.Background(), manager)
	if got := ManagerFromContext(ctx); got != manager {
		t.Fatalf("ManagerFromContext() = %p, want %p", got, manager)
	}
	if got := ManagerFromContext(context.Background()); got != nil {
		t.Fatalf("ManagerFromContext(background) = %p, want nil", got)
	}
}

func TestPublishRecordIsolatesScopedManagers(t *testing.T) {
	first := NewManager(1)
	second := NewManager(1)
	var firstCount atomic.Int64
	var secondCount atomic.Int64
	first.RegisterCriticalNamed("collector", testPluginFunc(func(context.Context, Record) {
		firstCount.Add(1)
	}))
	second.RegisterCriticalNamed("collector", testPluginFunc(func(context.Context, Record) {
		secondCount.Add(1)
	}))

	PublishRecord(WithManager(context.Background(), first), Record{Model: "first"})
	if firstCount.Load() != 1 || secondCount.Load() != 0 {
		t.Fatalf("after first publish: first=%d second=%d", firstCount.Load(), secondCount.Load())
	}
	PublishRecord(WithManager(context.Background(), second), Record{Model: "second"})
	if firstCount.Load() != 1 || secondCount.Load() != 1 {
		t.Fatalf("after second publish: first=%d second=%d", firstCount.Load(), secondCount.Load())
	}
}
