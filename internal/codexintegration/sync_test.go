package codexintegration

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

type fakeSyncRegistry struct {
	mu        sync.RWMutex
	models    []map[string]any
	providers map[string][]string
	hooks     map[int]registry.ModelRegistryHook
	nextID    int
}

func (fake *fakeSyncRegistry) GetAvailableModels(string) []map[string]any {
	fake.mu.RLock()
	defer fake.mu.RUnlock()
	return append([]map[string]any(nil), fake.models...)
}

func (fake *fakeSyncRegistry) GetProvidersForModel(model string) []string {
	fake.mu.RLock()
	defer fake.mu.RUnlock()
	return append([]string(nil), fake.providers[model]...)
}

func (fake *fakeSyncRegistry) SubscribeHook(hook registry.ModelRegistryHook) func() {
	fake.mu.Lock()
	if fake.hooks == nil {
		fake.hooks = make(map[int]registry.ModelRegistryHook)
	}
	fake.nextID++
	id := fake.nextID
	fake.hooks[id] = hook
	fake.mu.Unlock()
	return func() {
		fake.mu.Lock()
		delete(fake.hooks, id)
		fake.mu.Unlock()
	}
}

func (fake *fakeSyncRegistry) notify() {
	fake.mu.RLock()
	hooks := make([]registry.ModelRegistryHook, 0, len(fake.hooks))
	for _, hook := range fake.hooks {
		hooks = append(hooks, hook)
	}
	fake.mu.RUnlock()
	for _, hook := range hooks {
		hook.OnModelsRegistered(context.Background(), "xai", "test", nil)
	}
}

func TestSyncWorkerDebouncesEventsAndKeepsLastGood(t *testing.T) {
	lifecycle := testLifecycle(t)
	lifecycle.Config.CodexIntegration.CodexHome = lifecycle.Paths.Home
	models, providerFunc := catalogTestModels()
	providers := make(map[string][]string)
	for _, model := range models {
		id := catalogString(model, "id")
		providers[id] = providerFunc(id)
	}
	fake := &fakeSyncRegistry{models: models, providers: providers}
	if _, err := lifecycle.Setup(models, providerFunc, true, false); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	before, err := os.ReadFile(lifecycle.Paths.CatalogFile)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewSyncWorker(lifecycle.Config, fake)
	if err != nil {
		t.Fatal(err)
	}
	worker.debounce = 20 * time.Millisecond
	worker.templateSubscribe = func(func(uint64)) func() { return func() {} }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		worker.Run(ctx)
		close(done)
	}()
	waitForSyncAttempts(t, worker, 1)

	for range 50 {
		fake.notify()
	}
	waitForSyncAttempts(t, worker, 2)
	time.Sleep(60 * time.Millisecond)
	if attempts := worker.Status().Attempts; attempts != 2 {
		t.Fatalf("event storm produced %d attempts, want 2 including initial", attempts)
	}

	fake.mu.Lock()
	filtered := make([]map[string]any, 0, len(fake.models))
	for _, model := range fake.models {
		if catalogString(model, "id") != "gemini-pro-agent" {
			filtered = append(filtered, model)
		}
	}
	fake.models = filtered
	fake.mu.Unlock()
	fake.notify()
	waitForSyncAttempts(t, worker, 3)
	if worker.Status().LastError == "" {
		t.Fatal("missing mapped model did not record sync error")
	}
	afterFailure, err := os.ReadFile(lifecycle.Paths.CatalogFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterFailure) != string(before) {
		t.Fatal("failed sync replaced last-good catalog")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SyncWorker did not stop after context cancellation")
	}
	beforeAttempts := worker.Status().Attempts
	fake.notify()
	time.Sleep(50 * time.Millisecond)
	if worker.Status().Attempts != beforeAttempts {
		t.Fatal("stopped SyncWorker processed a later event")
	}
}

func waitForSyncAttempts(t *testing.T, worker *SyncWorker, want uint64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if worker.Status().Attempts >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("sync attempts = %d, want at least %d", worker.Status().Attempts, want)
}
