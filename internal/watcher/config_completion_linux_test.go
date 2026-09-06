//go:build linux

package watcher

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"golang.org/x/sys/unix"
)

func TestConfigCompletionOverflowReconcilesAndEnablesFallback(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 9002\nauth-dir: "+tmp+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	reloaded := make(chan int, 1)
	w := &Watcher{configPath: configPath, authDir: tmp, reloadCallback: func(cfg *config.Config) { reloaded <- cfg.Port }}
	w.SetConfig(&config.Config{Port: 9001, AuthDir: tmp})
	w.completionActive.Store(true)

	buf := make([]byte, unix.SizeofInotifyEvent)
	binary.NativeEndian.PutUint32(buf[4:8], unix.IN_Q_OVERFLOW)
	completion := &configCompletionWatcher{targets: []completionTarget{{base: filepath.Base(configPath), wd: 0}}}
	err := completion.handleEvents(buf, func() { t.Fatal("overflow invoked normal completion callback") })
	if err == nil {
		t.Fatal("overflow was not reported as unavailable")
	}
	w.configCompletionUnavailable(err)
	if w.completionActive.Load() {
		t.Fatal("overflow did not enable fsnotify fallback")
	}
	select {
	case port := <-reloaded:
		if port != 9002 {
			t.Fatalf("overflow reconciliation loaded port %d, want 9002", port)
		}
	case <-time.After(time.Second):
		t.Fatal("overflow did not reconcile current config")
	}
}

func TestStartCapturesConfigChangedAfterNativeAdmission(t *testing.T) {
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.Mkdir(authDir, 0o755); err != nil {
		t.Fatalf("create auth dir: %v", err)
	}
	configPath := filepath.Join(tmpDir, "config.yaml")
	writeConfig := func(port int) {
		t.Helper()
		if err := os.WriteFile(configPath, []byte(fmt.Sprintf("port: %d\nauth-dir: %s\n", port, authDir)), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}
	writeConfig(5001)
	reloads := make(chan int, 2)
	w, err := NewWatcher(configPath, authDir, func(cfg *config.Config) { reloads <- cfg.Port })
	if err != nil {
		t.Fatalf("create watcher: %v", err)
	}
	w.SetConfig(&config.Config{Port: 5001, AuthDir: authDir})
	w.startTestHook = func() { writeConfig(5002) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err = w.Start(ctx); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer func() { _ = w.Stop() }()

	deadline := time.After(time.Second)
	for {
		select {
		case port := <-reloads:
			if port == 5002 {
				return
			}
		case <-deadline:
			t.Fatal("native admission change was not observed")
		}
	}
}

func TestConfigWatcherReloadsSymlinkTargetInPlace(t *testing.T) {
	linkDir := t.TempDir()
	targetDir := t.TempDir()
	authDir := filepath.Join(linkDir, "auth")
	if err := os.Mkdir(authDir, 0o755); err != nil {
		t.Fatalf("create auth dir: %v", err)
	}
	targetPath := filepath.Join(targetDir, "real-config.yaml")
	linkPath := filepath.Join(linkDir, "config.yaml")
	writeTarget := func(port int) {
		t.Helper()
		if err := os.WriteFile(targetPath, []byte(fmt.Sprintf("port: %d\nauth-dir: %s\n", port, authDir)), 0o644); err != nil {
			t.Fatalf("write target config: %v", err)
		}
	}
	writeTarget(7001)
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Fatalf("create config symlink: %v", err)
	}

	reloads := make(chan int, 2)
	w, err := NewWatcher(linkPath, authDir, func(cfg *config.Config) { reloads <- cfg.Port })
	if err != nil {
		t.Fatalf("create watcher: %v", err)
	}
	w.SetConfig(&config.Config{Port: 7001, AuthDir: authDir})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err = w.Start(ctx); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer func() { _ = w.Stop() }()
	select {
	case <-reloads:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial callback")
	}

	f, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("open target config: %v", err)
	}
	if _, err = f.WriteString("port: 7002\nauth-dir:"); err != nil {
		t.Fatalf("write target prefix: %v", err)
	}
	select {
	case port := <-reloads:
		_ = f.Close()
		t.Fatalf("reloaded symlink target port %d before close", port)
	case <-time.After(2 * configReloadDebounce):
	}
	if _, err = f.WriteString(" " + authDir + "\n"); err != nil {
		_ = f.Close()
		t.Fatalf("write target suffix: %v", err)
	}
	if err = f.Close(); err != nil {
		t.Fatalf("close target config: %v", err)
	}
	select {
	case port := <-reloads:
		if port != 7002 {
			t.Fatalf("symlink target loaded port %d, want 7002", port)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("symlink target in-place write was not reloaded")
	}
}

func TestConfigWatcherReloadsSymlinkTargetAtomicReplacements(t *testing.T) {
	linkDir := t.TempDir()
	targetDir := t.TempDir()
	authDir := filepath.Join(linkDir, "auth")
	if err := os.Mkdir(authDir, 0o755); err != nil {
		t.Fatalf("create auth dir: %v", err)
	}
	targetPath := filepath.Join(targetDir, "real-config.yaml")
	linkPath := filepath.Join(linkDir, "config.yaml")
	writeConfig := func(path string, port int) {
		t.Helper()
		if err := os.WriteFile(path, []byte(fmt.Sprintf("port: %d\nauth-dir: %s\n", port, authDir)), 0o644); err != nil {
			t.Fatalf("write target config: %v", err)
		}
	}
	writeConfig(targetPath, 7101)
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Fatalf("create config symlink: %v", err)
	}

	reloads := make(chan int, 4)
	w, err := NewWatcher(linkPath, authDir, func(cfg *config.Config) { reloads <- cfg.Port })
	if err != nil {
		t.Fatalf("create watcher: %v", err)
	}
	w.SetConfig(&config.Config{Port: 7101, AuthDir: authDir})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err = w.Start(ctx); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer func() { _ = w.Stop() }()
	select {
	case <-reloads:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial callback")
	}

	for _, port := range []int{7102, 7103} {
		tmp := filepath.Join(targetDir, fmt.Sprintf(".real-config-%d.tmp", port))
		writeConfig(tmp, port)
		if err = os.Rename(tmp, targetPath); err != nil {
			t.Fatalf("replace target config: %v", err)
		}
		select {
		case got := <-reloads:
			if got != port {
				t.Fatalf("symlink target loaded port %d, want %d", got, port)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("symlink target replacement %d was not reloaded", port)
		}
	}
}

func TestConfigCompletionUnavailableEnablesFallback(t *testing.T) {
	w := &Watcher{}
	w.completionActive.Store(true)
	w.configCompletionUnavailable(errors.New("lost"))
	if w.completionActive.Load() {
		t.Fatal("expected native completion to be disabled")
	}
}

func TestConfigCompletionWatcherLifecycle(t *testing.T) {
	missingConfig := filepath.Join(t.TempDir(), "missing", "config.yaml")
	completion, err := newConfigCompletionWatcher(missingConfig)
	if err != nil {
		t.Fatalf("create completion watcher: %v", err)
	}
	if err = completion.Start(context.Background(), func() {}, func(error) {}, nil); err == nil {
		t.Fatal("expected completion watcher start to fail for missing directory")
	}
	if err = completion.Close(); err != nil {
		t.Fatalf("close failed completion watcher: %v", err)
	}

	dir := t.TempDir()
	completion, err = newConfigCompletionWatcher(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("create completion watcher: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err = completion.Start(ctx, func() {}, func(error) {}, nil); err != nil {
		t.Fatalf("start completion watcher: %v", err)
	}
	cancel()
	if err = completion.Close(); err != nil {
		t.Fatalf("close completion watcher: %v", err)
	}
	if err = completion.Close(); err != nil {
		t.Fatalf("second close completion watcher: %v", err)
	}
}

func TestConfigWatcherReloadsRepeatedAtomicReplacements(t *testing.T) {
	testConfigWatcherReloadsRepeatedAtomicReplacements(t, true)
}

func TestConfigWatcherWaitsForInPlaceWriterClose(t *testing.T) {
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.Mkdir(authDir, 0o755); err != nil {
		t.Fatalf("create auth dir: %v", err)
	}
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf("port: 2001\nauth-dir: %s\n", authDir)), 0o644); err != nil {
		t.Fatalf("write initial config: %v", err)
	}

	reloads := make(chan int, 4)
	w, err := NewWatcher(configPath, authDir, func(cfg *config.Config) { reloads <- cfg.Port })
	if err != nil {
		t.Fatalf("create watcher: %v", err)
	}
	w.SetConfig(&config.Config{Port: 2001, AuthDir: authDir})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err = w.Start(ctx); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer func() { _ = w.Stop() }()

	select {
	case <-reloads: // initial client load
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial callback")
	}

	f, err := os.OpenFile(configPath, os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("open config: %v", err)
	}
	if _, err = f.WriteString("port: 2002\nauth-dir:"); err != nil {
		t.Fatalf("write prefix: %v", err)
	}

	select {
	case got := <-reloads:
		_ = f.Close()
		t.Fatalf("reloaded port %d before writer closed", got)
	case <-time.After(2 * configReloadDebounce):
	}

	if _, err = f.WriteString(" " + authDir + "\n"); err != nil {
		_ = f.Close()
		t.Fatalf("write suffix: %v", err)
	}
	if err = f.Close(); err != nil {
		t.Fatalf("close config: %v", err)
	}

	select {
	case got := <-reloads:
		if got != 2002 {
			t.Fatalf("expected final port 2002, got %d", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for close-triggered reload")
	}
}

func TestConfigWatcherWaitsForNewInPlaceFileClose(t *testing.T) {
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.Mkdir(authDir, 0o755); err != nil {
		t.Fatalf("create auth dir: %v", err)
	}
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf("port: 3001\nauth-dir: %s\n", authDir)), 0o644); err != nil {
		t.Fatalf("write initial config: %v", err)
	}
	reloads := make(chan int, 4)
	w, err := NewWatcher(configPath, authDir, func(cfg *config.Config) { reloads <- cfg.Port })
	if err != nil {
		t.Fatalf("create watcher: %v", err)
	}
	w.SetConfig(&config.Config{Port: 3001, AuthDir: authDir})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err = w.Start(ctx); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer func() { _ = w.Stop() }()
	select {
	case <-reloads:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial callback")
	}
	if err = os.Remove(configPath); err != nil {
		t.Fatalf("remove config: %v", err)
	}
	f, err := os.OpenFile(configPath, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("recreate config: %v", err)
	}
	if _, err = f.WriteString("port: 3002\nauth-dir:"); err != nil {
		t.Fatalf("write prefix: %v", err)
	}
	select {
	case got := <-reloads:
		_ = f.Close()
		t.Fatalf("reloaded port %d on create before close", got)
	case <-time.After(2 * configReloadDebounce):
	}
	if _, err = f.WriteString(" " + authDir + "\n"); err != nil {
		_ = f.Close()
		t.Fatalf("write suffix: %v", err)
	}
	if err = f.Close(); err != nil {
		t.Fatalf("close config: %v", err)
	}
	select {
	case got := <-reloads:
		if got != 3002 {
			t.Fatalf("expected final port 3002, got %d", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for recreated file close")
	}
}
