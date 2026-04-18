package management

import (
	"context"
	"testing"
	"time"
)

type recordingAPICallListener struct {
	ch chan ManagementAPICallEvent
}

func (l *recordingAPICallListener) OnManagementAPICall(_ context.Context, evt ManagementAPICallEvent) {
	l.ch <- evt
}

func TestManagementAPICallEventBusDispatchesAsync(t *testing.T) {
	t.Parallel()

	bus := newManagementAPICallEventBus()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	bus.Register(ManagementAPICallListenerFunc(func(context.Context, ManagementAPICallEvent) {
		entered <- struct{}{}
		<-release
	}))

	done := make(chan struct{})
	go func() {
		bus.Publish(context.Background(), ManagementAPICallEvent{AuthIndex: "a1", StatusCode: 200})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("publish blocked by listener")
	}

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("listener did not receive event")
	}
	close(release)
}

func TestManagementAPICallEventBusDeliversToAllListeners(t *testing.T) {
	t.Parallel()

	bus := newManagementAPICallEventBus()
	first := &recordingAPICallListener{ch: make(chan ManagementAPICallEvent, 1)}
	second := &recordingAPICallListener{ch: make(chan ManagementAPICallEvent, 1)}
	bus.Register(first)
	bus.Register(second)

	want := ManagementAPICallEvent{AuthIndex: "a-multi", StatusCode: 201}
	bus.Publish(context.Background(), want)

	select {
	case got := <-first.ch:
		if got.AuthIndex != want.AuthIndex || got.StatusCode != want.StatusCode {
			t.Fatalf("first listener got %+v, want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("first listener missing event")
	}

	select {
	case got := <-second.ch:
		if got.AuthIndex != want.AuthIndex || got.StatusCode != want.StatusCode {
			t.Fatalf("second listener got %+v, want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("second listener missing event")
	}
}

func TestManagementAPICallEventBusRecoversPanics(t *testing.T) {
	t.Parallel()

	bus := newManagementAPICallEventBus()
	bus.Register(ManagementAPICallListenerFunc(func(context.Context, ManagementAPICallEvent) {
		panic("boom")
	}))
	listener := &recordingAPICallListener{ch: make(chan ManagementAPICallEvent, 1)}
	bus.Register(listener)

	bus.Publish(context.Background(), ManagementAPICallEvent{AuthIndex: "a2"})

	select {
	case <-listener.ch:
	case <-time.After(time.Second):
		t.Fatal("expected non-panicking listener to still receive event")
	}
}
