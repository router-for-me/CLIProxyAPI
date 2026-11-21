//go:build linux

package watcher

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/atomicfile"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// recordEventsUntilSentinel runs action, then creates a sentinel file, and returns
// the events for name inotify delivered before the sentinel's: events on one watch
// arrive in order, so the sentinel marks the end of action's events.
func recordEventsUntilSentinel(t *testing.T, dir, name string, action func()) []fsnotify.Event {
	t.Helper()
	fsWatcher, errNew := fsnotify.NewWatcher()
	if errNew != nil {
		t.Fatalf("fsnotify.NewWatcher: %v", errNew)
	}
	defer func() {
		if errClose := fsWatcher.Close(); errClose != nil {
			t.Errorf("close fsnotify watcher: %v", errClose)
		}
	}()
	if errAdd := fsWatcher.Add(dir); errAdd != nil {
		t.Fatalf("watch %s: %v", dir, errAdd)
	}
	action()
	sentinel := filepath.Join(dir, "sentinel")
	if errWrite := os.WriteFile(sentinel, nil, 0o600); errWrite != nil {
		t.Fatalf("write sentinel: %v", errWrite)
	}

	// The timer only guards against a hang.
	hangGuard := time.NewTimer(30 * time.Second)
	defer hangGuard.Stop()
	var events []fsnotify.Event
	for {
		select {
		case event := <-fsWatcher.Events:
			if event.Name == sentinel {
				return events
			}
			if event.Name == name {
				events = append(events, event)
			}
		case errWatch := <-fsWatcher.Errors:
			t.Fatalf("fsnotify error: %v", errWatch)
		case <-hangGuard.C:
			t.Fatal("sentinel event never arrived")
		}
	}
}

// On Linux, atomicfile.ReplaceFile swaps the file in with RENAME_EXCHANGE, which
// inotify reports as a Rename of the target. handleEvent must treat those real
// events as a replace (no settle delay, client kept) and still process a deletion
// right after.
func TestExchangeSaveEventsAreHandledAsReplace(t *testing.T) {
	var waits atomic.Int32
	original := waitForReplace
	waitForReplace = func() { waits.Add(1) }
	t.Cleanup(func() { waitForReplace = original })

	authDir := t.TempDir()
	authFile := filepath.Join(authDir, "exchange.json")
	oldContent := []byte(`{"type":"demo","v":1}`)
	if err := os.WriteFile(authFile, oldContent, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	newContent := []byte(`{"type":"demo","v":2}`)
	events := recordEventsUntilSentinel(t, authDir, authFile, func() {
		if err := atomicfile.ReplaceFile(authFile, newContent, 0o600); err != nil {
			t.Fatalf("ReplaceFile() error = %v", err)
		}
	})
	sawRename := false
	for _, event := range events {
		t.Logf("event for target: %s", event.Op)
		if event.Op&fsnotify.Rename != 0 {
			sawRename = true
		}
	}
	if !sawRename {
		t.Skip("this filesystem did not report the exchange as a Rename of the target")
	}

	sum := sha256.Sum256(newContent)
	w := &Watcher{
		authDir:        authDir,
		config:         &config.Config{AuthDir: authDir},
		lastAuthHashes: make(map[string]string),
	}
	key := w.normalizeAuthPath(authFile)
	w.lastAuthHashes[key] = hexString(sum[:])
	for _, event := range events {
		w.handleEvent(event)
	}
	if n := waits.Load(); n != 0 {
		t.Fatalf("exchange save events waited %d times for a replace, want 0", n)
	}
	if _, ok := w.lastAuthHashes[key]; !ok {
		t.Fatal("exchange save events removed the auth file")
	}

	removal := recordEventsUntilSentinel(t, authDir, authFile, func() {
		if err := os.Remove(authFile); err != nil {
			t.Fatalf("remove: %v", err)
		}
	})
	for _, event := range removal {
		w.handleEvent(event)
	}
	if _, ok := w.lastAuthHashes[key]; ok {
		t.Fatal("deletion right after an exchange save was not processed")
	}
}
