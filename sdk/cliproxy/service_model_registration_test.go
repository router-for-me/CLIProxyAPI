package cliproxy

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestRegisterModelsForAuthBatch_StaleSnapshotPreservesExcludedModels(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	raw := []byte(`{"type":"codex","email":"blocked@example.com","access_token":"diagnostic-invalid-token","plan_type":"pro","excluded_models":["gpt-6-astra","gpt-6.1-sol"]}`)
	path := filepath.Join(dir, "blocked.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(dir)
	manager := coreauth.NewManager(store, nil, nil)
	if err := manager.Load(ctx); err != nil {
		t.Fatal(err)
	}
	service := &Service{cfg: &config.Config{}, coreManager: manager}
	auths := manager.List()
	if len(auths) != 1 {
		t.Fatalf("loaded %d auths, want 1", len(auths))
	}
	old := auths[0]
	if old.Attributes["excluded_models"] != "" {
		t.Fatal("initial file load unexpectedly populated derived exclusion attribute")
	}
	reg := GlobalModelRegistry()
	t.Cleanup(func() { reg.UnregisterClient(old.ID) })
	started, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	modelRegistrationTaskHook = func() {
		if first.CompareAndSwap(false, true) {
			close(started)
			<-resume
		}
	}
	var resumeOnce sync.Once
	release := func() { resumeOnce.Do(func() { close(resume) }) }
	t.Cleanup(func() {
		release()
		select {
		case <-done:
			modelRegistrationTaskHook = nil
		case <-time.After(5 * time.Second):
			t.Error("batch did not finish during cleanup")
		}
	})
	// Hold the startup snapshot until the watcher has registered the exclusions.
	go func() {
		defer close(done)
		service.registerModelsForAuthBatch(ctx, []*coreauth.Auth{old})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("batch did not start")
	}
	synthesized, err := synthesizer.SynthesizeAuthFile(&synthesizer.SynthesisContext{Config: service.cfg, AuthDir: dir, Now: time.Now()}, path, raw)
	if err != nil || len(synthesized) != 1 {
		t.Fatalf("synthesize: %v", err)
	}
	current, err := manager.Update(coreauth.WithSkipPersist(ctx), synthesized[0])
	if err != nil {
		t.Fatal(err)
	}
	service.completeModelRegistrationForAuth(ctx, current)
	for _, model := range []string{"gpt-6-astra", "gpt-6.1-sol"} {
		if reg.ClientSupportsModel(old.ID, model) {
			t.Fatalf("exclusion initially failed: %s", model)
		}
	}
	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("batch did not finish")
	}
	live, ok := manager.GetByID(old.ID)
	if !ok || live.Attributes["excluded_models"] != "gpt-6-astra,gpt-6.1-sol" {
		t.Fatal("current auth lost exclusions")
	}
	if !reg.ClientSupportsModel(old.ID, "gpt-6-luna") {
		t.Error("non-excluded model was removed")
	}
	for _, model := range []string{"gpt-6-astra", "gpt-6.1-sol"} {
		if reg.ClientSupportsModel(old.ID, model) {
			t.Errorf("stale batch restored excluded model: %s", model)
		}
	}
}
