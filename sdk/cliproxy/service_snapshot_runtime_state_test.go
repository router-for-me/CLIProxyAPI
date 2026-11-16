package cliproxy

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// Service-level repro on dev: a file snapshot applied through handleAuthUpdates (the
// production path) after a 429 on the same auth keeps the cooldown.
func TestHandleAuthUpdatesFileSnapshotKeepsCooldown(t *testing.T) {
	for _, model := range []string{"claude-sonnet-4-6", ""} {
		name := model
		if name == "" {
			name = "auth_level"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			manager := coreauth.NewManager(nil, nil, nil)
			service, errBuild := NewBuilder().WithConfig(&internalconfig.Config{AuthDir: dir}).WithConfigPath(filepath.Join(dir, "config.yaml")).WithCoreAuthManager(manager).Build()
			if errBuild != nil {
				t.Fatal(errBuild)
			}
			id := "cooldown.json"
			path := filepath.Join(dir, id)
			raw := `{"type":"claude","email":"a@b.c"}`
			read := func() *coreauth.Auth {
				t.Helper()
				if errWrite := os.WriteFile(path, []byte(raw), 0o600); errWrite != nil {
					t.Fatal(errWrite)
				}
				auths, errSynth := synthesizer.SynthesizeAuthFile(&synthesizer.SynthesisContext{
					Config: &internalconfig.Config{}, AuthDir: dir, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator(),
				}, path, []byte(raw))
				if errSynth != nil || len(auths) != 1 {
					t.Fatalf("synthesize: %v %v", errSynth, auths)
				}
				return auths[0]
			}
			ctx := coreauth.WithSkipPersist(context.Background())
			service.handleAuthUpdates(ctx, []watcher.AuthUpdate{{Action: watcher.AuthUpdateActionAdd, ID: id, Auth: read()}})
			if model != "" {
				// Establish a healthy per-model state before the 429 so a pre-429 clone has non-empty ModelStates.
				manager.MarkResult(ctx, coreauth.Result{AuthID: id, Provider: "claude", Model: model, Success: true})
			}
			pre429Clone, _ := manager.GetByID(id)
			snapshot := read()
			if pre429Clone != nil && len(pre429Clone.ModelStates) > 0 {
				snapshot.ModelStates = pre429Clone.ModelStates
			}
			retry := time.Minute
			manager.MarkResult(ctx, coreauth.Result{AuthID: id, Provider: "claude", Model: model, RetryAfter: &retry,
				Error: &coreauth.Error{Message: "rate limited", HTTPStatus: http.StatusTooManyRequests}})
			want, _ := manager.GetByID(id)
			if !want.Unavailable || !want.Quota.Exceeded {
				t.Fatalf("no cooldown after the 429: %+v", want)
			}
			before := want.Generation
			service.handleAuthUpdates(ctx, []watcher.AuthUpdate{{Action: watcher.AuthUpdateActionModify, ID: id, Auth: snapshot}})
			got, _ := manager.GetByID(id)
			if got.Generation == before {
				t.Skip("snapshot was discarded by the fence")
			}
			if !got.Unavailable || !got.Quota.Exceeded || got.NextRetryAfter.IsZero() {
				t.Fatalf("applied snapshot reset the cooldown: unavailable=%v quota=%+v retry=%v", got.Unavailable, got.Quota, got.NextRetryAfter)
			}
			if model != "" {
				state := got.ModelStates[model]
				if state == nil || !state.Unavailable || !state.Quota.Exceeded || state.NextRetryAfter.IsZero() {
					t.Fatalf("applied snapshot reset model cooldown for %s: %+v", model, state)
				}
			}
		})
	}
}

func TestHandleAuthUpdatesDiscardedSnapshotTriggersWatcherReread(t *testing.T) {
	dir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	service, errBuild := NewBuilder().WithConfig(&internalconfig.Config{AuthDir: dir}).WithConfigPath(filepath.Join(dir, "config.yaml")).WithCoreAuthManager(manager).Build()
	if errBuild != nil {
		t.Fatal(errBuild)
	}
	var reread *coreauth.Auth
	service.watcher = &WatcherWrapper{
		rereadDiscardedFileSnapshot: func(auth *coreauth.Auth) {
			reread = auth
		},
	}

	id := "acct.json"
	path := filepath.Join(dir, id)
	initial := `{"type":"codex","email":"a@b.c","prefix":"pfx-0"}`
	edit := `{"type":"codex","email":"a@b.c","prefix":"pfx-1"}`
	synthesize := func(raw string) *coreauth.Auth {
		t.Helper()
		if errWrite := os.WriteFile(path, []byte(raw), 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
		auths, errSynth := synthesizer.SynthesizeAuthFile(&synthesizer.SynthesisContext{
			Config: &internalconfig.Config{}, AuthDir: dir, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator(),
		}, path, []byte(raw))
		if errSynth != nil || len(auths) != 1 {
			t.Fatalf("synthesize: %v %v", errSynth, auths)
		}
		return auths[0]
	}

	ctx := coreauth.WithSkipPersist(context.Background())
	service.handleAuthUpdates(ctx, []watcher.AuthUpdate{{Action: watcher.AuthUpdateActionAdd, ID: id, Auth: synthesize(initial)}})
	snapshot := synthesize(edit)

	// Truncate the file in place so the digest fence discards snapshot during handleAuthUpdates.
	file, errOpen := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			t.Errorf("close file: %v", errClose)
		}
	}()

	service.handleAuthUpdates(ctx, []watcher.AuthUpdate{{Action: watcher.AuthUpdateActionModify, ID: id, Auth: snapshot}})
	if reread == nil || !reread.SameFileSnapshot(snapshot) {
		t.Fatalf("reread = %+v, want discarded snapshot for %s", reread, id)
	}
}

func TestRuntimeAuthSyncHook_DelayedPersistedSnapshotCannotUndoDisable(t *testing.T) {
	runDelayedLoginAfterDisable(t, "")
}

func TestRuntimeAuthSyncHook_DiscardedLoginCacheCannotUndoDisableOnReload(t *testing.T) {
	for name, reload := range map[string]string{
		"forced reload":   "force-model-prefix: true\n",
		"provider reload": "oauth-excluded-models:\n  claude: [m]\n",
	} {
		t.Run(name, func(t *testing.T) { runDelayedLoginAfterDisable(t, reload) })
	}
}

func runDelayedLoginAfterDisable(t *testing.T, reload string) {
	dir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("auth-dir: "+dir+"\n"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg := &config.Config{AuthDir: dir}
	path := filepath.Join(dir, "claude-relogin.json")
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(dir)
	manager := coreauth.NewManager(store, nil, nil)
	service := &Service{cfg: cfg, coreManager: manager}
	w, errWatcher := watcher.NewWatcher(configPath, dir, nil)
	if errWatcher != nil {
		t.Fatal(errWatcher)
	}
	t.Cleanup(func() { _ = w.Stop() })
	w.SetConfig(cfg)
	queue := make(chan watcher.AuthUpdate, 8)
	w.SetAuthUpdateQueue(queue)
	drain := func() {
		w.DispatchRuntimeAuthUpdate(watcher.AuthUpdate{Action: watcher.AuthUpdateActionDelete, ID: "drain-marker"})
		for update := range queue {
			if update.ID == "drain-marker" {
				return
			}
			service.handleAuthUpdates(coreauth.WithSkipPersist(context.Background()), []watcher.AuthUpdate{update})
		}
	}
	service.watcher = &WatcherWrapper{
		dispatchPersistedAuthWithRev: func(update *watcher.AuthUpdate) (bool, uint64) {
			ok, rev := w.DispatchPersistedAuthUpdateWithRevision(update)
			drain()
			return ok, rev
		},
		rereadDiscardedFileSnapshot: w.RereadDiscardedFileSnapshot,
	}
	login := func(token string) *coreauth.Auth {
		data := []byte(`{"type":"claude","email":"user@example.com","access_token":"` + token + `"}`)
		if errWrite := os.WriteFile(path, data, 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
		auths, errSynth := synthesizer.SynthesizeAuthFile(&synthesizer.SynthesisContext{
			Config: cfg, AuthDir: dir, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator(),
		}, path, data)
		if errSynth != nil || len(auths) != 1 {
			t.Fatalf("synthesize: %v %v", errSynth, auths)
		}
		return auths[0]
	}
	hook := service.runtimeAuthSyncHook()
	if errHook := hook(context.Background(), login("t0")); errHook != nil {
		t.Fatal(errHook)
	}
	loggedIn := login("t1")
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(loggedIn.ID) })

	current, _ := manager.GetByID(loggedIn.ID)
	disabled, committed, errModify := manager.Modify(context.Background(), current.ID, current.RegistrationEpoch, func(a *coreauth.Auth) error {
		a.Disabled, a.Status = true, coreauth.StatusDisabled
		return nil
	})
	if errModify != nil || !committed {
		t.Fatalf("operator disable: committed=%v err=%v", committed, errModify)
	}
	if errHook := hook(context.Background(), disabled); errHook != nil {
		t.Fatal(errHook)
	}
	if errHook := hook(context.Background(), loggedIn); errHook != nil {
		t.Fatal(errHook)
	}
	drain()
	if got, _ := manager.GetByID(loggedIn.ID); !got.Disabled {
		t.Fatalf("delayed login snapshot undid the operator disable: status=%s token=%v", got.Status, got.Metadata["access_token"])
	}
	if reload == "" {
		return
	}

	if errChmod := os.Chmod(path, 0); errChmod != nil {
		t.Fatal(errChmod)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if _, errRead := os.ReadFile(path); errRead == nil {
		if errRestore := os.Chmod(path, 0o600); errRestore != nil {
			t.Fatal(errRestore)
		}
		if errWrite := os.WriteFile(path, []byte("{"), 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
	if errWrite := os.WriteFile(configPath, []byte("auth-dir: "+dir+"\n"+reload), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	w.ReloadConfigIfChanged()
	drain()
	if got, _ := manager.GetByID(loggedIn.ID); !got.Disabled {
		t.Fatalf("reload republished the discarded login snapshot over the disable: status=%s token=%v", got.Status, got.Metadata["access_token"])
	}
}
