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

func TestRegisterClient_NormalizesCopilotContextLength(t *testing.T) {
	r := newTestModelRegistry()
	hook := &capturingHook{
		registeredCh:   make(chan registeredCall, 1),
		unregisteredCh: make(chan unregisteredCall, 1),
	}
	r.SetHook(hook)

	r.RegisterClient("client-copilot", "github-copilot", []*ModelInfo{
		{ID: "gpt-5", ContextLength: 200000},
		{ID: "gpt-5-mini", ContextLength: 1048576},
	})

	select {
	case call := <-hook.registeredCh:
		for _, model := range call.models {
			if model.ContextLength != 128000 {
				t.Fatalf("hook model %q context_length=%d, want 128000", model.ID, model.ContextLength)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for OnModelsRegistered hook call")
	}

	registration, ok := r.models["gpt-5"]
	if !ok || registration == nil || registration.Info == nil {
		t.Fatal("expected gpt-5 registration info")
	}
	if registration.Info.ContextLength != 128000 {
		t.Fatalf("registry info context_length=%d, want 128000", registration.Info.ContextLength)
	}

	clientInfo, ok := r.clientModelInfos["client-copilot"]["gpt-5-mini"]
	if !ok || clientInfo == nil {
		t.Fatal("expected client model info for gpt-5-mini")
	}
	if clientInfo.ContextLength != 128000 {
		t.Fatalf("client model info context_length=%d, want 128000", clientInfo.ContextLength)
	}
}
