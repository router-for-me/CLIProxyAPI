package cliproxy

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Without a watcher (Home mode), a registration dropped for obsolete global exclusions
// must still be replaced by the re-registration that runs when the config is applied.
func TestModelRegistrationDroppedForGlobalExclusionsIsReplacedWithoutWatcher(t *testing.T) {
	models := registry.GetClaudeModels()
	if len(models) < 2 {
		t.Fatal("need two Claude models to test exclusion changes")
	}
	modelA, modelB := models[0].ID, models[1].ID
	oldCfg := &config.Config{OAuthExcludedModels: map[string][]string{"claude": {modelA}}}
	newCfg := &config.Config{OAuthExcludedModels: map[string][]string{"claude": {modelB}}}
	service := &Service{cfg: oldCfg, coreManager: coreauth.NewManager(nil, nil, nil), pluginHost: pluginhost.New()}
	service.applyPprofConfigContextFn = func(context.Context, *config.Config) bool { return true }
	t.Cleanup(func() {
		sdkAuth.RegisterPluginAuthParser(nil)
		sdktranslator.SetPluginHooks(nil)
	})

	var reads atomic.Int32
	oldStarted, oldRelease, oldDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseOld := sync.OnceFunc(func() { close(oldRelease) })
	auth, errRegister := service.coreManager.Register(t.Context(), &coreauth.Auth{
		ID: t.Name(), Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth"},
		// Pause only the first registration after it captured its fence.
		Runtime: &registrationInterleaveRuntime{onRead: func() {
			if reads.Add(1) == 1 {
				close(oldStarted)
				<-oldRelease
			}
		}},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	reg := GlobalModelRegistry()
	reg.RegisterClient(auth.ID, "claude", models[:2])
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	epoch := reg.ClientRegistrationEpoch(auth.ID)

	go func() {
		defer close(oldDone)
		service.registerModelsForAuth(t.Context(), auth)
	}()
	t.Cleanup(func() { releaseOld(); <-oldDone })
	<-oldStarted

	// Worst case: the obsolete registration finishes after the commit but before the runtime apply.
	commit := service.commitConfigUpdate(newCfg)
	if commit.cfg != newCfg {
		t.Fatal("config update was rejected")
	}
	releaseOld()
	<-oldDone
	if got := reg.ClientRegistrationEpoch(auth.ID); got != epoch {
		t.Fatalf("obsolete registration advanced epoch from %d to %d", epoch, got)
	}

	if !service.applyConfigRuntime(t.Context(), commit, true) {
		t.Fatal("config runtime apply failed")
	}
	if !reg.ClientSupportsModel(auth.ID, modelA) || reg.ClientSupportsModel(auth.ID, modelB) {
		t.Fatalf("final models: %s=%t, %s=%t; want new global exclusions", modelA, reg.ClientSupportsModel(auth.ID, modelA), modelB, reg.ClientSupportsModel(auth.ID, modelB))
	}
}

func TestModelRegistrationFenceRetainsGlobalExclusionsAfterAuthUpdate(t *testing.T) {
	for _, withManager := range []bool{false, true} {
		t.Run(map[bool]string{false: "without-manager", true: "with-manager"}[withManager], func(t *testing.T) {
			oldCfg := &config.Config{OAuthExcludedModels: map[string][]string{"claude": {"model-a"}}}
			newCfg := &config.Config{OAuthExcludedModels: map[string][]string{"claude": {"model-b"}}}
			service := &Service{cfg: oldCfg}
			auth := &coreauth.Auth{ID: t.Name(), Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth"}}
			if withManager {
				service.coreManager = coreauth.NewManager(nil, nil, nil)
				var errRegister error
				auth, errRegister = service.coreManager.Register(t.Context(), auth)
				if errRegister != nil {
					t.Fatal(errRegister)
				}
			}
			reg := GlobalModelRegistry()
			reg.RegisterClient(auth.ID, "claude", []*ModelInfo{{ID: "model-a"}})
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
			epoch := reg.ClientRegistrationEpoch(auth.ID)
			fence := newModelRegistrationFence(auth, oldCfg)
			if commit := service.commitConfigUpdate(newCfg); commit.cfg != newCfg {
				t.Fatal("config update was rejected")
			}
			auth = auth.Clone()
			auth.Attributes["note"] = "updated by plugin"
			if withManager {
				var errUpdate error
				auth, errUpdate = service.coreManager.Update(t.Context(), auth)
				if errUpdate != nil {
					t.Fatal(errUpdate)
				}
			}
			// Updating auth inputs must not let an old plugin result adopt the new config.
			fence = fence.withAuth(auth)
			if fence.publishModels(service, auth.ID, "claude", []*ModelInfo{{ID: "model-b"}}) {
				t.Error("obsolete config published models after auth update")
			}
			if fence.publishUnregister(service, auth.ID) {
				t.Error("obsolete config removed models after auth update")
			}
			if got := reg.ClientRegistrationEpoch(auth.ID); got != epoch {
				t.Fatalf("obsolete config advanced epoch from %d to %d", epoch, got)
			}
			// A caller supplying an old config explicitly must not capture a fresh identity.
			service.registerModelsForAuthWithConfigAndCache(t.Context(), oldCfg, auth, nil)
			if got := reg.ClientRegistrationEpoch(auth.ID); got != epoch {
				t.Fatalf("explicit stale config advanced epoch from %d to %d", epoch, got)
			}
			current := newModelRegistrationFence(auth, newCfg)
			if !current.publishModels(service, auth.ID, "claude", []*ModelInfo{{ID: "model-a"}}) {
				t.Fatal("current config could not publish after obsolete work was rejected")
			}
		})
	}
}

func TestModelRegistrationAllowsIrrelevantConfigReload(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rules    map[string][]string
		kind     string
		own      string
		disabled bool
	}{
		{"no-op", map[string][]string{"claude": {"model-a"}}, "oauth", "", false},
		{"normalized no-op", map[string][]string{"claude": {" MODEL-A ", "model-a", " "}}, "oauth", "", false},
		{"other provider", map[string][]string{"claude": {"model-a"}, "codex": {"model-b"}}, "oauth", "", false},
		{"credential override", map[string][]string{"claude": {"model-b"}}, "oauth", "own-*", false},
		{"api key", map[string][]string{"claude": {"model-b"}}, "apikey", "", false},
		{"disabled", map[string][]string{"claude": {"model-b"}}, "oauth", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldCfg := &config.Config{OAuthExcludedModels: map[string][]string{"claude": {"model-a"}}}
			// A debug-only change must not discard work that has no replacement scheduled.
			newCfg := &config.Config{Debug: true, OAuthExcludedModels: tc.rules}
			service := &Service{cfg: oldCfg, coreManager: coreauth.NewManager(nil, nil, nil)}
			auth, errRegister := service.coreManager.Register(t.Context(), &coreauth.Auth{
				ID: t.Name(), Provider: "claude", Disabled: tc.disabled,
				Attributes: map[string]string{"auth_kind": tc.kind, "excluded_models": tc.own},
			})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			reg := GlobalModelRegistry()
			reg.RegisterClient(auth.ID, "claude", []*ModelInfo{{ID: "model-old"}})
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
			fence := newModelRegistrationFence(auth, oldCfg)
			if commit := service.commitConfigUpdate(newCfg); commit.cfg != newCfg {
				t.Fatal("config update was rejected")
			}
			if tc.disabled {
				if !fence.publishUnregister(service, auth.ID) {
					t.Fatal("global rules prevented removal of a disabled credential")
				}
			} else if !fence.publishModels(service, auth.ID, "claude", []*ModelInfo{{ID: "allowed-model"}}) {
				t.Fatal("irrelevant config change discarded valid registration")
			}
		})
	}
}

func TestModelRegistrationSnapshotsGlobalExclusionValues(t *testing.T) {
	cfg := &config.Config{OAuthExcludedModels: map[string][]string{"claude": {"model-a"}}}
	service := &Service{cfg: cfg}
	auth := &coreauth.Auth{ID: t.Name(), Provider: "claude"}
	reg := GlobalModelRegistry()
	reg.RegisterClient(auth.ID, "claude", []*ModelInfo{{ID: "model-a"}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	fence := newModelRegistrationFence(auth, cfg)
	epoch := reg.ClientRegistrationEpoch(auth.ID)
	// Capture rule values, not just config/map identity.
	service.cfgMu.Lock()
	cfg.OAuthExcludedModels["claude"][0] = "model-b"
	service.cfgMu.Unlock()
	if fence.publishModels(service, auth.ID, "claude", []*ModelInfo{{ID: "model-b"}}) {
		t.Fatal("obsolete exclusion values were accepted after config mutation")
	}
	if got := reg.ClientRegistrationEpoch(auth.ID); got != epoch {
		t.Fatalf("rejected registration advanced epoch from %d to %d", epoch, got)
	}
}

func TestModelRegistrationRejectsObsoleteGlobalExclusions(t *testing.T) {
	models := registry.GetClaudeModels()
	if len(models) < 2 {
		t.Fatal("need two Claude models to test exclusion changes")
	}
	modelA, modelB := models[0].ID, models[1].ID
	for _, oldEmpty := range []bool{false, true} {
		for _, oldFirst := range []bool{true, false} {
			name := map[bool]string{false: "publish", true: "unregister"}[oldEmpty]
			name += map[bool]string{false: "/new-first", true: "/old-first"}[oldFirst]
			t.Run(name, func(t *testing.T) {
				oldRule := modelA
				if oldEmpty {
					oldRule = "*"
				}
				oldCfg := &config.Config{OAuthExcludedModels: map[string][]string{"claude": {oldRule}}}
				newCfg := &config.Config{OAuthExcludedModels: map[string][]string{"claude": {modelB}}}
				manager := coreauth.NewManager(nil, nil, nil)
				service := &Service{cfg: oldCfg, coreManager: manager}
				path := filepath.Join(t.TempDir(), "credential.json")
				data := []byte(`{"type":"claude"}`)
				// Manager.Update validates file-synthesized snapshots against disk.
				if errWrite := os.WriteFile(path, data, 0o600); errWrite != nil {
					t.Fatal(errWrite)
				}
				synthesize := func(cfg *config.Config) *coreauth.Auth {
					t.Helper()
					auths, errSynthesize := synthesizer.SynthesizeAuthFile(&synthesizer.SynthesisContext{
						Config: cfg, Now: time.Date(2026, time.October, 10, 0, 0, 0, 0, time.UTC),
					}, path, data)
					if errSynthesize != nil || len(auths) != 1 {
						t.Fatalf("synthesis = (%v, %v)", auths, errSynthesize)
					}
					return auths[0]
				}
				oldAuth := synthesize(oldCfg)
				reg := GlobalModelRegistry()
				reg.RegisterClient(oldAuth.ID, "claude", models[:2])
				t.Cleanup(func() { reg.UnregisterClient(oldAuth.ID) })
				epoch := reg.ClientRegistrationEpoch(oldAuth.ID)

				oldStarted, oldRelease, oldDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
				releaseOld := sync.OnceFunc(func() { close(oldRelease) })
				oldAuth.Runtime = &registrationInterleaveRuntime{onRead: func() {
					close(oldStarted)
					<-oldRelease
				}}
				registered, errRegister := manager.Register(t.Context(), oldAuth)
				if errRegister != nil {
					t.Fatal(errRegister)
				}
				go func() {
					defer close(oldDone)
					service.registerModelsForAuth(t.Context(), registered)
				}()
				t.Cleanup(func() { releaseOld(); <-oldDone })
				<-oldStarted

				// Reload while the old registration is paused after capturing its fence.
				if commit := service.commitConfigUpdate(newCfg); commit.cfg != newCfg {
					t.Fatal("config update was rejected")
				}
				newAuth := synthesize(newCfg)
				newStarted, newRelease, newDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
				releaseNew := sync.OnceFunc(func() { close(newRelease) })
				newAuth.Runtime = &registrationInterleaveRuntime{onRead: func() {
					close(newStarted)
					<-newRelease
				}}
				updated, errUpdate := manager.Update(t.Context(), newAuth)
				if errUpdate != nil {
					t.Fatal(errUpdate)
				}
				go func() {
					defer close(newDone)
					service.registerModelsForAuth(t.Context(), updated)
				}()
				t.Cleanup(func() { releaseNew(); <-newDone })
				<-newStarted

				if oldFirst {
					releaseOld()
					<-oldDone
					if got := reg.ClientRegistrationEpoch(oldAuth.ID); got != epoch {
						t.Errorf("obsolete registration advanced epoch from %d to %d", epoch, got)
					}
					releaseNew()
					<-newDone
				} else {
					releaseNew()
					<-newDone
					releaseOld()
					<-oldDone
				}
				if !reg.ClientSupportsModel(oldAuth.ID, modelA) || reg.ClientSupportsModel(oldAuth.ID, modelB) {
					t.Fatalf("final models: %s=%t, %s=%t; want new global exclusions", modelA, reg.ClientSupportsModel(oldAuth.ID, modelA), modelB, reg.ClientSupportsModel(oldAuth.ID, modelB))
				}
			})
		}
	}
}
