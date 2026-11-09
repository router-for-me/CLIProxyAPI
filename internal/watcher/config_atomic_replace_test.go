package watcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// Exercise real filesystem notifications rather than manually invoking reload.
// The bounded ticker waits for observable completion, not a guessed sleep.
func TestRuntimeConfigReloadSurvivesRepeatedAtomicReplacement(t *testing.T) {
	root := t.TempDir()
	authDir := filepath.Join(root, "auth")
	if errMkdir := os.Mkdir(authDir, 0o700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	configPath := filepath.Join(root, "config.yaml")
	writeConfig := func(path string, port int) []byte {
		t.Helper()
		// Omitted options use the real loader defaults instead of marshaled zero values.
		data := []byte(fmt.Sprintf("auth-dir: %q\nport: %d\n", authDir, port))
		if errWrite := os.WriteFile(path, data, 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
		return data
	}
	writeConfig(configPath, 8317)
	initial, errLoad := config.LoadConfig(configPath)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	w, errNew := NewWatcher(configPath, authDir, nil)
	if errNew != nil {
		t.Fatal(errNew)
	}
	// These tests exercise local files, not an optional process-wide token store.
	w.storePersister = nil
	w.mirroredAuthDir = ""
	w.SetConfig(initial)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		if errStop := w.Stop(); errStop != nil {
			t.Error(errStop)
		}
	})
	if errStart := w.Start(ctx); errStart != nil {
		t.Fatal(errStart)
	}
	for _, step := range []struct {
		name    string
		port    int
		replace bool
	}{
		{"first replacement", 8318, true},
		{"second replacement", 8319, true},
		{"in-place write after replacement", 8320, false},
	} {
		t.Run(step.name, func(t *testing.T) {
			path := configPath
			if step.replace {
				path = filepath.Join(root, "config.pending")
			}
			data := writeConfig(path, step.port)
			if step.replace {
				if errRename := os.Rename(path, configPath); errRename != nil {
					t.Fatal(errRename)
				}
			}
			sum := sha256.Sum256(data)
			wantHash := hex.EncodeToString(sum[:])
			deadline := time.NewTimer(10 * time.Second)
			defer deadline.Stop()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				w.clientsMutex.RLock()
				done := w.config != nil && w.config.Port == step.port && w.lastConfigHash == wantHash
				w.clientsMutex.RUnlock()
				if done {
					return
				}
				select {
				case <-ticker.C:
				case <-deadline.C:
					t.Fatalf("filesystem reload did not apply port %d", step.port)
				}
			}
		})
	}
}

func TestRuntimeConfigDirectoryIgnoresUnrelatedFiles(t *testing.T) {
	root := t.TempDir()
	w := &Watcher{configPath: filepath.Join(root, "config.yaml"), authDir: filepath.Join(root, "auth")}
	for _, name := range []string{"config.pending", "unrelated.yaml", "unrelated.json"} {
		w.handleEvent(fsnotify.Event{Name: filepath.Join(root, name), Op: fsnotify.Write | fsnotify.Create})
	}
	w.configReloadMu.Lock()
	defer w.configReloadMu.Unlock()
	if w.configReloadTimer != nil {
		w.configReloadTimer.Stop()
		t.Fatal("unrelated sibling files must not schedule configuration reloads")
	}
}
