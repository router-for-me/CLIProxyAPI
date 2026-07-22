package registry

import (
	"context"
	"sync"
	"testing"
	"time"
)

func newTestModelRegistry() *ModelRegistry {
	return &ModelRegistry{
		models:           make(map[string]*ModelRegistration),
		clientModels:     make(map[string][]string),
		clientModelInfos: make(map[string]map[string]*ModelInfo),
		clientProviders:  make(map[string]string),
		mutex:            &sync.RWMutex{},
	}
}

type registeredCall struct {
	provider string
	clientID string
	models   []*ModelInfo
}

type unregisteredCall struct {
	provider string
	clientID string
}

type capturingHook struct {
	registeredCh   chan registeredCall
	unregisteredCh chan unregisteredCall
}

func (h *capturingHook) OnModelsRegistered(ctx context.Context, provider, clientID string, models []*ModelInfo) {
	h.registeredCh <- registeredCall{provider: provider, clientID: clientID, models: models}
}

func (h *capturingHook) OnModelsUnregistered(ctx context.Context, provider, clientID string) {
	h.unregisteredCh <- unregisteredCall{provider: provider, clientID: clientID}
}

func TestModelRegistryHook_OnModelsRegisteredCalled(t *testing.T) {
	r := newTestModelRegistry()
	hook := &capturingHook{
		registeredCh:   make(chan registeredCall, 1),
		unregisteredCh: make(chan unregisteredCall, 1),
	}
	r.SetHook(hook)

	inputModels := []*ModelInfo{
		{ID: "m1", DisplayName: "Model One"},
		{ID: "m2", DisplayName: "Model Two"},
	}
	r.RegisterClient("client-1", "OpenAI", inputModels)

	select {
	case call := <-hook.registeredCh:
		if call.provider != "openai" {
			t.Fatalf("provider mismatch: got %q, want %q", call.provider, "openai")
		}
		if call.clientID != "client-1" {
			t.Fatalf("clientID mismatch: got %q, want %q", call.clientID, "client-1")
		}
		if len(call.models) != 2 {
			t.Fatalf("models length mismatch: got %d, want %d", len(call.models), 2)
		}
		if call.models[0] == nil || call.models[0].ID != "m1" {
			t.Fatalf("models[0] mismatch: got %#v, want ID=%q", call.models[0], "m1")
		}
		if call.models[1] == nil || call.models[1].ID != "m2" {
			t.Fatalf("models[1] mismatch: got %#v, want ID=%q", call.models[1], "m2")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for OnModelsRegistered hook call")
	}
}

func TestModelRegistryHook_OnModelsUnregisteredCalled(t *testing.T) {
	r := newTestModelRegistry()
	hook := &capturingHook{
		registeredCh:   make(chan registeredCall, 1),
		unregisteredCh: make(chan unregisteredCall, 1),
	}
	r.SetHook(hook)

	r.RegisterClient("client-1", "OpenAI", []*ModelInfo{{ID: "m1"}})
	select {
	case <-hook.registeredCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for OnModelsRegistered hook call")
	}

	r.UnregisterClient("client-1")

	select {
	case call := <-hook.unregisteredCh:
		if call.provider != "openai" {
			t.Fatalf("provider mismatch: got %q, want %q", call.provider, "openai")
		}
		if call.clientID != "client-1" {
			t.Fatalf("clientID mismatch: got %q, want %q", call.clientID, "client-1")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for OnModelsUnregistered hook call")
	}
}

type blockingHook struct {
	started chan struct{}
	unblock chan struct{}
}

func (h *blockingHook) OnModelsRegistered(ctx context.Context, provider, clientID string, models []*ModelInfo) {
	select {
	case <-h.started:
	default:
		close(h.started)
	}
	<-h.unblock
}

func (h *blockingHook) OnModelsUnregistered(ctx context.Context, provider, clientID string) {}

func TestModelRegistryHook_DoesNotBlockRegisterClient(t *testing.T) {
	r := newTestModelRegistry()
	hook := &blockingHook{
		started: make(chan struct{}),
		unblock: make(chan struct{}),
	}
	r.SetHook(hook)
	defer close(hook.unblock)

	done := make(chan struct{})
	go func() {
		r.RegisterClient("client-1", "OpenAI", []*ModelInfo{{ID: "m1"}})
		close(done)
	}()

	select {
	case <-hook.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for hook to start")
	}

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("RegisterClient appears to be blocked by hook")
	}

	if !r.ClientSupportsModel("client-1", "m1") {
		t.Fatal("model registration failed; expected client to support model")
	}
}

type panicHook struct {
	registeredCalled   chan struct{}
	unregisteredCalled chan struct{}
}

func (h *panicHook) OnModelsRegistered(ctx context.Context, provider, clientID string, models []*ModelInfo) {
	if h.registeredCalled != nil {
		h.registeredCalled <- struct{}{}
	}
	panic("boom")
}

func (h *panicHook) OnModelsUnregistered(ctx context.Context, provider, clientID string) {
	if h.unregisteredCalled != nil {
		h.unregisteredCalled <- struct{}{}
	}
	panic("boom")
}

func TestModelRegistryHook_PanicDoesNotAffectRegistry(t *testing.T) {
	r := newTestModelRegistry()
	hook := &panicHook{
		registeredCalled:   make(chan struct{}, 1),
		unregisteredCalled: make(chan struct{}, 1),
	}
	r.SetHook(hook)

	r.RegisterClient("client-1", "OpenAI", []*ModelInfo{{ID: "m1"}})

	select {
	case <-hook.registeredCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for OnModelsRegistered hook call")
	}

	if !r.ClientSupportsModel("client-1", "m1") {
		t.Fatal("model registration failed; expected client to support model")
	}

	r.UnregisterClient("client-1")

	select {
	case <-hook.unregisteredCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for OnModelsUnregistered hook call")
	}
}

func TestModelRegistrySubscribersPreserveLegacyHookAndCanCancel(t *testing.T) {
	r := newTestModelRegistry()
	legacy := &capturingHook{
		registeredCh:   make(chan registeredCall, 2),
		unregisteredCh: make(chan unregisteredCall, 2),
	}
	subscriber := &capturingHook{
		registeredCh:   make(chan registeredCall, 2),
		unregisteredCh: make(chan unregisteredCall, 2),
	}
	r.SetHook(legacy)
	cancel := r.SubscribeHook(subscriber)
	r.RegisterClient("client-1", "xai", []*ModelInfo{{ID: "grok-4.5"}})

	for name, channel := range map[string]<-chan registeredCall{
		"legacy": legacy.registeredCh, "subscriber": subscriber.registeredCh,
	} {
		select {
		case call := <-channel:
			if call.provider != "xai" || call.clientID != "client-1" || len(call.models) != 1 {
				t.Fatalf("%s call = %#v", name, call)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for %s hook", name)
		}
	}

	cancel()
	cancel()
	r.RegisterClient("client-2", "xai", []*ModelInfo{{ID: "grok-build-0.1"}})
	select {
	case call := <-subscriber.registeredCh:
		t.Fatalf("cancelled subscriber received %#v", call)
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case <-legacy.registeredCh:
	case <-time.After(2 * time.Second):
		t.Fatal("legacy hook stopped after subscriber cancellation")
	}
}

type mutatingHook struct {
	done chan struct{}
}

func (h *mutatingHook) OnModelsRegistered(_ context.Context, _, _ string, models []*ModelInfo) {
	if len(models) > 0 {
		models[0].ID = "mutated"
	}
	close(h.done)
}

func (h *mutatingHook) OnModelsUnregistered(context.Context, string, string) {}

func TestModelRegistrySubscribersReceiveIndependentSnapshots(t *testing.T) {
	r := newTestModelRegistry()
	mutator := &mutatingHook{done: make(chan struct{})}
	observer := &capturingHook{registeredCh: make(chan registeredCall, 1), unregisteredCh: make(chan unregisteredCall, 1)}
	r.SubscribeHook(mutator)
	r.SubscribeHook(observer)
	r.RegisterClient("client-1", "xai", []*ModelInfo{{ID: "grok-4.5"}})
	select {
	case <-mutator.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for mutating subscriber")
	}
	select {
	case call := <-observer.registeredCh:
		if len(call.models) != 1 || call.models[0].ID != "grok-4.5" {
			t.Fatalf("observer snapshot was mutated: %#v", call.models)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for observer")
	}
}
