package watcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fsnotify/fsnotify"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

func TestWatcherAuthDirSwitchPreservesNestedWatches(t *testing.T) {
	for _, mode := range []string{"SetConfig", "reloadConfig"} {
		t.Run(mode, func(t *testing.T) {
			tmp := t.TempDir()
			dirA := filepath.Join(tmp, "auth-a")
			dirB := filepath.Join(tmp, "auth-b")
			for _, dir := range []string{dirA, dirB} {
				if err := os.MkdirAll(filepath.Join(dir, "team"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			pathA := filepath.Join(dirA, "team", "account.json")
			pathB := filepath.Join(dirB, "team", "account.json")
			for path, token := range map[string]string{pathA: "old", pathB: "new"} {
				if err := os.WriteFile(path, codexWatcherAuth(t, token), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			configPath := filepath.Join(tmp, "config.yaml")
			writeConfig := func(dir string) {
				t.Helper()
				data, err := yaml.Marshal(&config.Config{AuthDir: dir, CredentialInFlight: config.DefaultCredentialInFlightConfig()})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(configPath, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			writeConfig(dirA)
			w, err := NewWatcher(configPath, dirA, nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(func() {
				cancel()
				if err := w.Stop(); err != nil {
					t.Error(err)
				}
			})
			queue := make(chan AuthUpdate, 16)
			w.SetConfig(&config.Config{AuthDir: dirA})
			w.SetAuthUpdateQueue(queue)
			if err := w.Start(ctx); err != nil {
				t.Fatal(err)
			}
			id := filepath.Join("team", "account.json")
			pending := make(map[string][]AuthUpdate)
			assertToken := func(want string) {
				t.Helper()
				update := receiveAuthUpdate(t, queue, id, pending)
				if update.Auth == nil || update.Auth.Metadata["access_token"] != want {
					t.Fatalf("nested credential = %#v, want token %q", update, want)
				}
			}
			assertToken("old")
			if mode == "SetConfig" {
				w.SetConfig(&config.Config{AuthDir: dirB})
				w.reloadClients(true, nil, false)
			} else {
				writeConfig(dirB)
				w.ReloadConfigIfChanged()
			}
			assertToken("new")
			assertAuthDirectoryNotWatched(t, w, dirA)
			assertAuthDirectoryNotWatched(t, w, filepath.Join(dirA, "team"))
			assertAuthDirectoryWatched(t, w, filepath.Join(dirB, "team"))

			// A queued directory event from the retired tree must not reinstall it.
			staleDir := filepath.Join(dirA, "team", "late")
			if err := os.Mkdir(staleDir, 0o700); err != nil {
				t.Fatal(err)
			}
			w.handleEvent(fsnotify.Event{Name: staleDir, Op: fsnotify.Create})
			assertAuthDirectoryNotWatched(t, w, staleDir)
			w.handleEvent(fsnotify.Event{Name: pathA, Op: fsnotify.Write})
			if err := os.WriteFile(pathB, codexWatcherAuth(t, "updated"), 0o600); err != nil {
				t.Fatal(err)
			}
			assertToken("updated")
			if err := os.Remove(pathB); err != nil {
				t.Fatal(err)
			}
			if update := receiveAuthUpdate(t, queue, id, pending); update.Action != AuthUpdateActionDelete {
				t.Fatalf("nested deletion = %#v", update)
			}
		})
	}
}
