package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func testConfigYAML(port int, authDir string) string {
	return fmt.Sprintf("port: %d\nauth-dir: %q\nserver:\n  server-id: \"11111111-1111-4111-8111-111111111111\"\n", port, authDir)
}

func replaceConfigByRename(t *testing.T, configPath, body string) {
	t.Helper()
	tmpFile, errCreate := os.CreateTemp(filepath.Dir(configPath), ".config-replace-*.yaml")
	if errCreate != nil {
		t.Fatalf("failed to create temp config file: %v", errCreate)
	}
	tmpPath := tmpFile.Name()
	if _, errWrite := tmpFile.WriteString(body); errWrite != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		t.Fatalf("failed to write temp config file: %v", errWrite)
	}
	if errClose := tmpFile.Close(); errClose != nil {
		_ = os.Remove(tmpPath)
		t.Fatalf("failed to close temp config file: %v", errClose)
	}
	if errRename := os.Rename(tmpPath, configPath); errRename != nil {
		_ = os.Remove(tmpPath)
		t.Fatalf("failed to rename temp config over %s: %v", configPath, errRename)
	}
}

func waitForReloadPort(t *testing.T, ports <-chan int, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case got := <-ports:
			if got == want {
				return
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for reload with port %d", want)
		}
	}
}

func waitForAuthUpdate(t *testing.T, queue <-chan AuthUpdate, wantAction AuthUpdateAction, wantID string, timeout time.Duration) AuthUpdate {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case update := <-queue:
			if update.Action == wantAction && update.ID == wantID {
				return update
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for auth update action=%s id=%s", wantAction, wantID)
		}
	}
}

func TestConfigWatchSurvivesRenameReplace(t *testing.T) {
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if errMkdir := os.MkdirAll(authDir, 0o755); errMkdir != nil {
		t.Fatalf("failed to create auth dir: %v", errMkdir)
	}
	configPath := filepath.Join(tmpDir, "config.yaml")
	configYAML := func(port int) string {
		return testConfigYAML(port, authDir)
	}
	if errWrite := os.WriteFile(configPath, []byte(configYAML(8000)), 0o600); errWrite != nil {
		t.Fatalf("failed to write initial config: %v", errWrite)
	}

	ports := make(chan int, 16)
	w, errNew := NewWatcher(configPath, authDir, func(cfg *config.Config) {
		if cfg != nil {
			ports <- cfg.Port
		}
	})
	if errNew != nil {
		t.Fatalf("NewWatcher failed: %v", errNew)
	}
	defer func() {
		if errStop := w.Stop(); errStop != nil {
			t.Errorf("Stop failed: %v", errStop)
		}
	}()
	w.SetConfig(&config.Config{Port: 8000, AuthDir: authDir})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if errStart := w.Start(ctx); errStart != nil {
		t.Fatalf("Start failed: %v", errStart)
	}
	waitForReloadPort(t, ports, 8000, 3*time.Second)

	// Confirm both the config parent directory (for atomic renames/recreation)
	// and the config file itself (for bind mounts and symlink targets) are watched.
	watchingConfigDir := false
	watchingConfigFile := false
	for _, watched := range w.watcher.WatchList() {
		if w.sameNormalizedAuthPath(watched, configPath) {
			watchingConfigFile = true
		}
		if w.sameNormalizedAuthPath(watched, filepath.Dir(configPath)) {
			watchingConfigDir = true
		}
	}
	if !watchingConfigDir || !watchingConfigFile {
		t.Fatalf("expected both config dir %s (%v) and config file %s (%v) in watch list %v",
			filepath.Dir(configPath), watchingConfigDir, configPath, watchingConfigFile, w.watcher.WatchList())
	}

	// 1. Atomic rename replacement over the original config file.
	replaceConfigByRename(t, configPath, configYAML(8001))
	waitForReloadPort(t, ports, 8001, 3*time.Second)

	// 2. Subsequent in-place write after atomic rename.
	if errWrite := os.WriteFile(configPath, []byte(configYAML(8002)), 0o600); errWrite != nil {
		t.Fatalf("failed to write config in place: %v", errWrite)
	}
	waitForReloadPort(t, ports, 8002, 3*time.Second)

	// 3. Another atomic rename replacement.
	replaceConfigByRename(t, configPath, configYAML(8003))
	waitForReloadPort(t, ports, 8003, 3*time.Second)

	// 4. Delayed replacement after the original file is removed.
	if errRemove := os.Remove(configPath); errRemove != nil {
		t.Fatalf("failed to remove config: %v", errRemove)
	}
	time.Sleep(configReloadDebounce + 100*time.Millisecond)
	replaceConfigByRename(t, configPath, configYAML(8004))
	waitForReloadPort(t, ports, 8004, 3*time.Second)

	// 5. Confirm file watch was re-attached to the new config inode.
	reattached := false
	for _, watched := range w.watcher.WatchList() {
		if w.sameNormalizedAuthPath(watched, configPath) {
			reattached = true
		}
	}
	if !reattached {
		t.Fatalf("expected config file %s to be re-attached in watch list %v", configPath, w.watcher.WatchList())
	}
}

func TestConfigWatchSurvivesAuthDirSwitchFromConfigDir(t *testing.T) {
	tmpDir := t.TempDir()
	dirB := filepath.Join(tmpDir, "auth-b")
	if errMkdir := os.MkdirAll(dirB, 0o755); errMkdir != nil {
		t.Fatalf("failed to create auth dir B: %v", errMkdir)
	}
	configPath := filepath.Join(tmpDir, "config.yaml")
	configYAML := testConfigYAML
	if errWrite := os.WriteFile(configPath, []byte(configYAML(8100, tmpDir)), 0o600); errWrite != nil {
		t.Fatalf("failed to write initial config: %v", errWrite)
	}

	ports := make(chan int, 16)
	w, errNew := NewWatcher(configPath, tmpDir, func(cfg *config.Config) {
		if cfg != nil {
			ports <- cfg.Port
		}
	})
	if errNew != nil {
		t.Fatalf("NewWatcher failed: %v", errNew)
	}
	defer func() {
		if errStop := w.Stop(); errStop != nil {
			t.Errorf("Stop failed: %v", errStop)
		}
	}()
	w.SetConfig(&config.Config{Port: 8100, AuthDir: tmpDir})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if errStart := w.Start(ctx); errStart != nil {
		t.Fatalf("Start failed: %v", errStart)
	}
	waitForReloadPort(t, ports, 8100, 3*time.Second)

	// Switch auth-dir away from config.yaml's parent directory.
	replaceConfigByRename(t, configPath, configYAML(8101, dirB))
	waitForReloadPort(t, ports, 8101, 3*time.Second)

	// Subsequent config edit must still be detected after the old auth-dir watch is removed.
	replaceConfigByRename(t, configPath, configYAML(8102, dirB))
	waitForReloadPort(t, ports, 8102, 3*time.Second)
}

func TestConfigWatchSharedDirMixedRelativeAndAbsolutePaths(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	absAuthDir, errAbs := filepath.Abs(tmpDir)
	if errAbs != nil {
		t.Fatalf("filepath.Abs failed: %v", errAbs)
	}
	relConfigPath := "./config.yaml"
	configYAML := func(port int) string {
		return testConfigYAML(port, absAuthDir)
	}
	if errWrite := os.WriteFile(relConfigPath, []byte(configYAML(8200)), 0o600); errWrite != nil {
		t.Fatalf("failed to write initial config: %v", errWrite)
	}

	ports := make(chan int, 16)
	queue := make(chan AuthUpdate, 16)
	w, errNew := NewWatcher(relConfigPath, absAuthDir, func(cfg *config.Config) {
		if cfg != nil {
			ports <- cfg.Port
		}
	})
	if errNew != nil {
		t.Fatalf("NewWatcher failed: %v", errNew)
	}
	defer func() {
		if errStop := w.Stop(); errStop != nil {
			t.Errorf("Stop failed: %v", errStop)
		}
	}()
	w.SetAuthUpdateQueue(queue)
	w.SetConfig(&config.Config{Port: 8200, AuthDir: absAuthDir})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if errStart := w.Start(ctx); errStart != nil {
		t.Fatalf("Start failed: %v", errStart)
	}
	waitForReloadPort(t, ports, 8200, 3*time.Second)

	// Auth JSON lifecycle in the shared directory must work with mixed relative config / absolute auth-dir.
	credPath := filepath.Join(absAuthDir, "mixed-cred.json")
	if errWrite := os.WriteFile(credPath, []byte(`{"type":"codex","email":"v1@example.com"}`), 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}
	waitForAuthUpdate(t, queue, AuthUpdateActionAdd, "mixed-cred.json", 3*time.Second)

	if errWrite := os.WriteFile(credPath, []byte(`{"type":"codex","email":"v2@example.com"}`), 0o600); errWrite != nil {
		t.Fatalf("failed to update auth file: %v", errWrite)
	}
	updated := waitForAuthUpdate(t, queue, AuthUpdateActionModify, "mixed-cred.json", 3*time.Second)
	if updated.Auth == nil || updated.Auth.Label != "v2@example.com" {
		t.Fatalf("expected updated label v2@example.com, got %+v", updated.Auth)
	}

	if errRemove := os.Remove(credPath); errRemove != nil {
		t.Fatalf("failed to remove auth file: %v", errRemove)
	}
	waitForAuthUpdate(t, queue, AuthUpdateActionDelete, "mixed-cred.json", 3*time.Second)

	// Atomic rename of the relative config path in the shared directory must also reload.
	replaceConfigByRename(t, relConfigPath, configYAML(8201))
	waitForReloadPort(t, ports, 8201, 3*time.Second)
}

func TestConfigWatchFollowsSymlinkTargetAndReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink test requires elevated privileges on Windows")
	}

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	linkDir := filepath.Join(tmpDir, "link")
	targetDir := filepath.Join(tmpDir, "target")
	for _, dir := range []string{authDir, linkDir, targetDir} {
		if errMkdir := os.MkdirAll(dir, 0o755); errMkdir != nil {
			t.Fatalf("failed to create dir %s: %v", dir, errMkdir)
		}
	}

	configYAML := func(port int) string {
		return testConfigYAML(port, authDir)
	}
	targetV1 := filepath.Join(targetDir, "live-v1.yaml")
	if errWrite := os.WriteFile(targetV1, []byte(configYAML(8300)), 0o600); errWrite != nil {
		t.Fatalf("failed to write target v1: %v", errWrite)
	}
	configLink := filepath.Join(linkDir, "config.yaml")
	if errLink := os.Symlink(targetV1, configLink); errLink != nil {
		t.Fatalf("failed to create config symlink: %v", errLink)
	}

	ports := make(chan int, 16)
	w, errNew := NewWatcher(configLink, authDir, func(cfg *config.Config) {
		if cfg != nil {
			ports <- cfg.Port
		}
	})
	if errNew != nil {
		t.Fatalf("NewWatcher failed: %v", errNew)
	}
	defer func() {
		if errStop := w.Stop(); errStop != nil {
			t.Errorf("Stop failed: %v", errStop)
		}
	}()
	w.SetConfig(&config.Config{Port: 8300, AuthDir: authDir})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if errStart := w.Start(ctx); errStart != nil {
		t.Fatalf("Start failed: %v", errStart)
	}
	waitForReloadPort(t, ports, 8300, 3*time.Second)

	// 1. In-place write to the symlink target in another directory must reload.
	if errWrite := os.WriteFile(targetV1, []byte(configYAML(8301)), 0o600); errWrite != nil {
		t.Fatalf("failed to update target v1: %v", errWrite)
	}
	waitForReloadPort(t, ports, 8301, 3*time.Second)

	// 2. Atomic symlink swap to a new target file must reload.
	targetV2 := filepath.Join(targetDir, "live-v2.yaml")
	if errWrite := os.WriteFile(targetV2, []byte(configYAML(8302)), 0o600); errWrite != nil {
		t.Fatalf("failed to write target v2: %v", errWrite)
	}
	tmpLink := filepath.Join(linkDir, ".config.yaml.next")
	if errLink := os.Symlink(targetV2, tmpLink); errLink != nil {
		t.Fatalf("failed to create temp symlink: %v", errLink)
	}
	if errRename := os.Rename(tmpLink, configLink); errRename != nil {
		t.Fatalf("failed to replace config symlink: %v", errRename)
	}
	if runtime.GOOS == "darwin" {
		// fsnotify's kqueue backend opens symlinks with O_EVTONLY and does not emit an event
		// when a symlink overwrites an already-seen symlink; inject the Create event Linux inotify emits
		// so we still exercise real re-attachment to targetV2 below.
		w.handleEvent(fsnotify.Event{Name: configLink, Op: fsnotify.Create})
	}
	waitForReloadPort(t, ports, 8302, 3*time.Second)

	// 3. In-place write to the new symlink target must also reload.
	if errWrite := os.WriteFile(targetV2, []byte(configYAML(8303)), 0o600); errWrite != nil {
		t.Fatalf("failed to update target v2: %v", errWrite)
	}
	waitForReloadPort(t, ports, 8303, 3*time.Second)
}

func TestConfigWatchFallsBackWhenConfigDirNotReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permission bits do not apply on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory read permission checks")
	}

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	restrictedDir := filepath.Join(tmpDir, "restricted")
	for _, dir := range []string{authDir, restrictedDir} {
		if errMkdir := os.MkdirAll(dir, 0o755); errMkdir != nil {
			t.Fatalf("failed to create dir %s: %v", dir, errMkdir)
		}
	}
	configPath := filepath.Join(restrictedDir, "config.yaml")
	configYAML := func(port int) string {
		return testConfigYAML(port, authDir)
	}
	if errWrite := os.WriteFile(configPath, []byte(configYAML(8400)), 0o600); errWrite != nil {
		t.Fatalf("failed to write initial config: %v", errWrite)
	}
	if errChmod := os.Chmod(restrictedDir, 0o111); errChmod != nil {
		t.Fatalf("failed to chmod restricted dir: %v", errChmod)
	}
	defer func() {
		_ = os.Chmod(restrictedDir, 0o755)
	}()

	ports := make(chan int, 16)
	w, errNew := NewWatcher(configPath, authDir, func(cfg *config.Config) {
		if cfg != nil {
			ports <- cfg.Port
		}
	})
	if errNew != nil {
		t.Fatalf("NewWatcher failed: %v", errNew)
	}
	defer func() {
		if errStop := w.Stop(); errStop != nil {
			t.Errorf("Stop failed: %v", errStop)
		}
	}()
	w.SetConfig(&config.Config{Port: 8400, AuthDir: authDir})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if errStart := w.Start(ctx); errStart != nil {
		t.Fatalf("expected Start to succeed with traverse-only config dir, got: %v", errStart)
	}
	waitForReloadPort(t, ports, 8400, 3*time.Second)

	if errWrite := os.WriteFile(configPath, []byte(configYAML(8401)), 0o600); errWrite != nil {
		t.Fatalf("failed to update config in place: %v", errWrite)
	}
	waitForReloadPort(t, ports, 8401, 3*time.Second)
}

func TestIsConfigEventPathMatching(t *testing.T) {
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if errMkdir := os.MkdirAll(authDir, 0o755); errMkdir != nil {
		t.Fatalf("failed to create auth dir: %v", errMkdir)
	}
	upperPath := filepath.Join(tmpDir, "Config.yaml")
	lowerPath := filepath.Join(tmpDir, "config.yaml")
	configYAML := func(port int) string {
		return testConfigYAML(port, authDir)
	}
	if errWrite := os.WriteFile(upperPath, []byte(configYAML(8500)), 0o600); errWrite != nil {
		t.Fatalf("failed to write Config.yaml: %v", errWrite)
	}

	w := &Watcher{configPath: lowerPath}

	// Relative vs absolute match.
	absConfigPath, errAbs := filepath.Abs("config.yaml")
	if errAbs != nil {
		t.Fatalf("filepath.Abs failed: %v", errAbs)
	}
	if !w.isConfigEventPath(w.normalizeAuthPath(absConfigPath), w.normalizeAuthPath("./config.yaml")) {
		t.Fatalf("expected absolute event path %q to match relative config path", absConfigPath)
	}
	if w.isConfigEventPath(w.normalizeAuthPath(filepath.Join(filepath.Dir(absConfigPath), "other.yaml")), w.normalizeAuthPath("./config.yaml")) {
		t.Fatal("expected unrelated file in same directory not to match config path")
	}

	// Case-insensitive volume: Config.yaml and config.yaml refer to the same file identity.
	infoUpper, errUpper := os.Stat(upperPath)
	infoLower, errLower := os.Stat(lowerPath)
	if errUpper == nil && errLower == nil && os.SameFile(infoUpper, infoLower) {
		if !w.isConfigEventPath(w.normalizeAuthPath(upperPath), w.normalizeAuthPath(lowerPath)) {
			t.Fatalf("expected case-variant %q to match %q on case-insensitive volume", upperPath, lowerPath)
		}
		// End-to-end check on case-insensitive volume: start with lowercase configPath while disk entry is Config.yaml.
		ports := make(chan int, 16)
		liveWatcher, errNew := NewWatcher(lowerPath, authDir, func(cfg *config.Config) {
			if cfg != nil {
				ports <- cfg.Port
			}
		})
		if errNew != nil {
			t.Fatalf("NewWatcher failed: %v", errNew)
		}
		defer func() {
			if errStop := liveWatcher.Stop(); errStop != nil {
				t.Errorf("Stop failed: %v", errStop)
			}
		}()
		liveWatcher.SetConfig(&config.Config{Port: 8500, AuthDir: authDir})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if errStart := liveWatcher.Start(ctx); errStart != nil {
			t.Fatalf("Start failed: %v", errStart)
		}
		waitForReloadPort(t, ports, 8500, 3*time.Second)
		replaceConfigByRename(t, upperPath, configYAML(8501))
		waitForReloadPort(t, ports, 8501, 3*time.Second)
	} else {
		// Case-sensitive volume: distinct Config.yaml must not match config.yaml.
		if errWriteLower := os.WriteFile(lowerPath, []byte(configYAML(8501)), 0o600); errWriteLower != nil {
			t.Fatalf("failed to write distinct config.yaml: %v", errWriteLower)
		}
		if w.isConfigEventPath(w.normalizeAuthPath(upperPath), w.normalizeAuthPath(lowerPath)) {
			t.Fatalf("expected distinct %q not to match %q on case-sensitive volume", upperPath, lowerPath)
		}
	}
}
