package registry

import (
	"context"
	"testing"
	"time"
)

func TestCatalogPolicyHotReload(t *testing.T) {
	oldGeneral, oldCodex, oldDevin := generalCatalogUpdater, codexCatalogUpdater, devinCatalogUpdater
	catalogRuntime.Lock()
	oldCtx, oldLocal := catalogRuntime.ctx, catalogRuntime.local
	catalogRuntime.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		generalCatalogUpdater, codexCatalogUpdater, devinCatalogUpdater = oldGeneral, oldCodex, oldDevin
		catalogRuntime.Lock()
		catalogRuntime.ctx, catalogRuntime.local = oldCtx, oldLocal
		catalogRuntime.Unlock()
	}()
	newUpdater := func() (*catalogUpdater, <-chan string) {
		published := make(chan string, 8)
		return &catalogUpdater{
			fetch:   func(ctx context.Context, source string) ([]byte, error) { return []byte(source), nil },
			publish: func(data []byte) ([]string, error) { published <- string(data); return nil, nil },
		}, published
	}
	var general, codex, devin <-chan string
	generalCatalogUpdater, general = newUpdater()
	codexCatalogUpdater, codex = newUpdater()
	devinCatalogUpdater, devin = newUpdater()
	awaitSource := func(ch <-chan string, want string) {
		t.Helper()
		select {
		case got := <-ch:
			if got != want {
				t.Fatalf("got %q want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("catalog source was not updated")
		}
	}
	SetLocalModelCatalogs(true)
	StartModelCatalogUpdaters(ctx, CatalogSources{Catalog: "https://example.com/general"}, false)
	awaitSource(general, "https://example.com/general")
	awaitSource(codex, embeddedCatalogSource)
	awaitSource(devin, embeddedCatalogSource)
	UpdateModelCatalogSources(CatalogSources{CodexCatalog: "https://example.com/codex"}, false)
	awaitSource(general, embeddedCatalogSource)
	awaitSource(codex, "https://example.com/codex")
	UpdateModelCatalogSources(CatalogSources{Catalog: "https://example.com/ignored"}, true)
	awaitSource(codex, embeddedCatalogSource)
	generalCatalogUpdater.mu.Lock()
	source := generalCatalogUpdater.source
	generalCatalogUpdater.mu.Unlock()
	if source != "disabled" {
		t.Fatal("Home allowed general catalog")
	}
	UpdateModelCatalogSources(CatalogSources{}, false)
	awaitSource(general, embeddedCatalogSource)

	// Shutdown must fence reloads, and a new SDK lifetime must retain CLI policy.
	cancel()
	codexCatalogUpdater.mu.Lock()
	generation := codexCatalogUpdater.generation
	codexCatalogUpdater.mu.Unlock()
	UpdateModelCatalogSources(CatalogSources{CodexCatalog: "https://example.com/stopped"}, false)
	codexCatalogUpdater.mu.Lock()
	stoppedGeneration := codexCatalogUpdater.generation
	codexCatalogUpdater.mu.Unlock()
	if stoppedGeneration != generation {
		t.Fatal("reload after shutdown restarted catalog readers")
	}
	restartCtx, restartCancel := context.WithCancel(context.Background())
	defer restartCancel()
	StartModelCatalogUpdaters(restartCtx, CatalogSources{Catalog: "https://example.com/ignored"}, true)
	awaitSource(codex, embeddedCatalogSource)
	awaitSource(devin, embeddedCatalogSource)
	generalCatalogUpdater.mu.Lock()
	source = generalCatalogUpdater.source
	generalCatalogUpdater.mu.Unlock()
	if source != "disabled" {
		t.Fatal("SDK startup in Home mode enabled the general catalog")
	}
}
