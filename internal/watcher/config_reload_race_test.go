package watcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

type reloadFixture struct {
	t          *testing.T
	path       string
	authDir    string
	watcher    *Watcher
	callbacks  atomic.Int32
	onCallback func(call int32)
}

func newReloadFixture(t *testing.T) *reloadFixture {
	t.Helper()
	dir := t.TempDir()
	f := &reloadFixture{t: t, path: filepath.Join(dir, "config.yaml"), authDir: filepath.Join(dir, "auth")}
	if err := os.MkdirAll(f.authDir, 0o755); err != nil {
		t.Fatalf("create auth dir: %v", err)
	}
	f.watcher = &Watcher{
		configPath: f.path,
		authDir:    f.authDir,
		reloadCallback: func(*config.Config) {
			call := f.callbacks.Add(1)
			if f.onCallback != nil {
				f.onCallback(call)
			}
		},
	}
	return f
}

func (f *reloadFixture) write(port int, extra string) {
	f.t.Helper()
	body := fmt.Sprintf("port: %d\nauth-dir: %q\n%s", port, f.authDir, extra)
	if err := os.WriteFile(f.path, []byte(body), 0o600); err != nil {
		f.t.Fatalf("write config: %v", err)
	}
}

func (f *reloadFixture) appliedPort() int {
	f.watcher.clientsMutex.RLock()
	defer f.watcher.clientsMutex.RUnlock()
	if f.watcher.config == nil {
		return 0
	}
	return f.watcher.config.Port
}

func (f *reloadFixture) recordedHash() string {
	f.watcher.clientsMutex.RLock()
	defer f.watcher.clientsMutex.RUnlock()
	return f.watcher.lastConfigHash
}

func (f *reloadFixture) fileHash() string {
	f.t.Helper()
	data, err := os.ReadFile(f.path)
	if err != nil {
		f.t.Fatalf("read config: %v", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type signalPersister struct {
	calls     atomic.Int32
	persisted chan struct{}
}

func (p *signalPersister) PersistConfig(context.Context) error {
	p.calls.Add(1)
	p.persisted <- struct{}{}
	return nil
}

func (p *signalPersister) PersistAuthFiles(context.Context, string, ...string) error { return nil }

// A save that lands while the previous one is being applied must be applied too,
// not recorded as already loaded.
func TestConfigReloadAppliesAWriteThatLandsDuringTheReload(t *testing.T) {
	f := newReloadFixture(t)
	f.write(8080, "")
	f.onCallback = func(call int32) {
		if call == 1 {
			f.write(9090, "")
		}
	}

	f.watcher.reloadConfigIfChanged()

	if got := f.appliedPort(); got != 9090 {
		t.Fatalf("applied port = %d, want the later save 9090", got)
	}
	if f.recordedHash() != f.fileHash() {
		t.Fatal("recorded hash does not match the applied file")
	}
	if got := f.callbacks.Load(); got != 2 {
		t.Fatalf("callbacks = %d, want 2", got)
	}
}

// A pass that applied content which is no longer on disk must not leave the earlier
// hash in place: a file restored to those earlier bytes still has to be loaded.
func TestConfigReloadReloadsAFileRestoredToItsRecordedContent(t *testing.T) {
	f := newReloadFixture(t)
	f.write(8080, "")
	f.watcher.reloadConfigIfChanged()
	recorded := f.recordedHash()

	f.write(9090, "")
	f.onCallback = func(call int32) {
		if call == 2 {
			f.write(8080, "")
		}
	}
	f.watcher.reloadConfigIfChanged()

	if got := f.appliedPort(); got != 8080 {
		t.Fatalf("applied port = %d, want the restored 8080", got)
	}
	if f.recordedHash() != recorded {
		t.Fatal("restored content is not recorded as applied")
	}
	if got := f.callbacks.Load(); got != 3 {
		t.Fatalf("callbacks = %d, want 3", got)
	}
}

// LoadConfig rewrites a plaintext secret-key as a bcrypt hash. The second pass reads
// that normalized file, records it, and the next change event for it is skipped.
func TestConfigReloadSettlesOnTheNormalizedFile(t *testing.T) {
	f := newReloadFixture(t)
	f.write(8080, "remote-management:\n  secret-key: \"plain-fixture-secret\"\n")
	persister := &signalPersister{persisted: make(chan struct{}, 4)}
	f.watcher.storePersister = persister

	f.watcher.reloadConfigIfChanged()

	data, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(data), "plain-fixture-secret") {
		t.Fatal("secret-key was not normalized")
	}
	if got := f.callbacks.Load(); got != 2 {
		t.Fatalf("callbacks = %d, want 2 (load, then load the normalized file)", got)
	}
	if f.recordedHash() != f.fileHash() {
		t.Fatal("normalized file is not recorded")
	}
	waitPersisted(t, persister)

	f.watcher.reloadConfigIfChanged()
	if got := f.callbacks.Load(); got != 2 {
		t.Fatalf("callbacks = %d after an unchanged event, want 2", got)
	}
}

// A file that changes during every pass is not acknowledged. The next event, once
// the writes stop, loads the final content.
func TestConfigReloadStopsAfterBoundedChurnAndRecoversOnTheNextEvent(t *testing.T) {
	f := newReloadFixture(t)
	f.write(8080, "")
	persister := &signalPersister{persisted: make(chan struct{}, 4)}
	f.watcher.storePersister = persister
	f.onCallback = func(call int32) { f.write(9000+int(call), "") }

	f.watcher.reloadConfigIfChanged()

	if got := f.callbacks.Load(); got != configReloadPasses {
		t.Fatalf("callbacks = %d, want %d", got, configReloadPasses)
	}
	if f.recordedHash() != "" {
		t.Fatal("unstable content was recorded as applied")
	}

	f.onCallback = nil
	f.watcher.reloadConfigIfChanged()

	if got, want := f.appliedPort(), 9000+configReloadPasses; got != want {
		t.Fatalf("applied port = %d, want %d", got, want)
	}
	if f.recordedHash() != f.fileHash() {
		t.Fatal("final content is not recorded")
	}
	waitPersisted(t, persister)
	if got := persister.calls.Load(); got != 1 {
		t.Fatalf("persist calls = %d, want only the stable reload", got)
	}
}

// Reloads from the debounce timer and from a management save must not overlap.
func TestConfigReloadRunsOneReloadAtATime(t *testing.T) {
	f := newReloadFixture(t)
	f.write(8080, "")
	var active, peak atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	f.onCallback = func(call int32) {
		now := active.Add(1)
		defer active.Add(-1)
		if now > peak.Load() {
			peak.Store(now)
		}
		if call == 1 {
			close(entered)
			<-release
		}
	}

	first := make(chan struct{})
	go func() { defer close(first); f.watcher.reloadConfigIfChanged() }()
	waitClosed(t, entered, "first reload")
	f.write(9090, "")
	second := make(chan struct{})
	go func() { defer close(second); f.watcher.reloadConfigIfChanged() }()
	close(release)
	waitClosed(t, first, "first reload to finish")
	waitClosed(t, second, "second reload to finish")

	if got := peak.Load(); got != 1 {
		t.Fatalf("overlapping reloads = %d, want 1", got)
	}
	if got := f.appliedPort(); got != 9090 {
		t.Fatalf("applied port = %d, want 9090", got)
	}
	if f.recordedHash() != f.fileHash() {
		t.Fatal("recorded hash does not match the applied file")
	}
}

func waitPersisted(t *testing.T, p *signalPersister) {
	t.Helper()
	select {
	case <-p.persisted:
	case <-time.After(5 * time.Second):
		t.Fatal("config was not persisted")
	}
}

func waitClosed(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}
