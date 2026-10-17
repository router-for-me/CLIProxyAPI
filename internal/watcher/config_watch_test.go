package watcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// setConfigPollInterval sets the periodic check's interval for watchers the test starts.
func setConfigPollInterval(t *testing.T, interval time.Duration) {
	t.Helper()
	original := configPollInterval
	configPollInterval = interval
	t.Cleanup(func() { configPollInterval = original })
}

// withoutConfigPolling keeps the periodic check out of a test, so only filesystem
// events can trigger the reloads it waits for.
func withoutConfigPolling(t *testing.T) {
	t.Helper()
	setConfigPollInterval(t, time.Hour)
}

func testConfigYAML(port int, authDir string) []byte {
	return []byte(fmt.Sprintf("port: %d\nauth-dir: %s\napi-keys:\n  - client-key\n", port, authDir))
}

func TestWatcherReloadsAfterConfigFileIsReplaced(t *testing.T) {
	withoutConfigPolling(t)
	for _, viaSymlink := range []bool{false, true} {
		t.Run(fmt.Sprintf("symlink=%t", viaSymlink), func(t *testing.T) {
			tmpDir := t.TempDir()
			authDir := filepath.Join(tmpDir, "auth")
			if err := os.MkdirAll(authDir, 0o755); err != nil {
				t.Fatalf("failed to create auth dir: %v", err)
			}
			configPath := filepath.Join(tmpDir, "config.yaml")
			filePath := configPath
			if viaSymlink {
				filePath = filepath.Join(tmpDir, "shared", "config.yaml")
				if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
					t.Fatalf("failed to create shared dir: %v", err)
				}
			}
			if err := os.WriteFile(filePath, testConfigYAML(8001, authDir), 0o600); err != nil {
				t.Fatalf("failed to write config: %v", err)
			}
			if viaSymlink {
				if err := os.Symlink(filePath, configPath); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}

			ports := make(chan int, 16)
			w, err := NewWatcher(configPath, authDir, func(cfg *config.Config) { ports <- cfg.Port })
			if err != nil {
				t.Fatalf("failed to create watcher: %v", err)
			}
			w.SetConfig(&config.Config{Port: 8001, AuthDir: authDir})
			ctx, cancel := context.WithCancel(context.Background())
			defer func() {
				cancel()
				if errStop := w.Stop(); errStop != nil {
					t.Errorf("failed to stop watcher: %v", errStop)
				}
			}()
			if err = w.Start(ctx); err != nil {
				t.Fatalf("failed to start watcher: %v", err)
			}

			// Editors and deployment tools often save by renaming a new file over the
			// config. The watch must survive the first replacement to see the second.
			for _, port := range []int{8002, 8003} {
				replaceFile(t, filePath, testConfigYAML(port, authDir))
				waitForReloadedPort(t, ports, port)
			}
		})
	}
}

func replaceFile(t *testing.T, path string, data []byte) {
	t.Helper()
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		t.Fatalf("failed to write replacement: %v", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		t.Fatalf("failed to replace %s: %v", path, err)
	}
}

func waitForReloadedPort(t *testing.T, ports <-chan int, want int) {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case got := <-ports:
			if got == want {
				return
			}
		case <-timeout:
			t.Fatalf("timed out waiting for a reload with port %d", want)
		}
	}
}

func TestWatcherWorksInAConfigDirectoryThatCannotBeListed(t *testing.T) {
	withoutConfigPolling(t)
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions do not restrict this user")
	}
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	configDir := filepath.Join(tmpDir, "conf")
	for _, dir := range []string{authDir, configDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("failed to create %s: %v", dir, err)
		}
	}
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(configPath, testConfigYAML(8001, authDir), 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	// Traversable and writable but not listable.
	if err := os.Chmod(configDir, 0o300); err != nil {
		t.Fatalf("failed to chmod config dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(configDir, 0o700); err != nil {
			t.Errorf("failed to restore config dir mode: %v", err)
		}
	})

	ports := make(chan int, 16)
	w, err := NewWatcher(configPath, authDir, func(cfg *config.Config) { ports <- cfg.Port })
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}
	w.SetConfig(&config.Config{Port: 8001, AuthDir: authDir})
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		if errStop := w.Stop(); errStop != nil {
			t.Errorf("failed to stop watcher: %v", errStop)
		}
	}()
	if err = w.Start(ctx); err != nil {
		t.Fatalf("failed to start watcher: %v", err)
	}

	if err = os.WriteFile(configPath, testConfigYAML(8002, authDir), 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	waitForReloadedPort(t, ports, 8002)
	// A replacement drops the file watch, so the file at the path is watched again.
	for _, port := range []int{8003, 8004} {
		replaceFile(t, configPath, testConfigYAML(port, authDir))
		waitForReloadedPort(t, ports, port)
	}
}

// startConfigWatcher starts a watcher on configPath and returns it with the ports its reloads apply.
func startConfigWatcher(t *testing.T, configPath, authDir string) (*Watcher, <-chan int) {
	t.Helper()
	ports := make(chan int, 16)
	w, err := NewWatcher(configPath, authDir, func(cfg *config.Config) { ports <- cfg.Port })
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}
	w.SetConfig(&config.Config{Port: 8001, AuthDir: authDir})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		if errStop := w.Stop(); errStop != nil {
			t.Errorf("failed to stop watcher: %v", errStop)
		}
	})
	if err = w.Start(ctx); err != nil {
		t.Fatalf("failed to start watcher: %v", err)
	}
	return w, ports
}

// retargetSymlink points link at target atomically, as `ln -sfn` and deployment tools do.
func retargetSymlink(t *testing.T, target, link string) {
	t.Helper()
	tmpLink := link + ".tmp"
	if err := os.Symlink(target, tmpLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Rename(tmpLink, link); err != nil {
		t.Fatalf("failed to retarget %s: %v", link, err)
	}
}

func TestWatcherAppliesARetargetedConfigSymlinkWithinOneInterval(t *testing.T) {
	setConfigPollInterval(t, 50*time.Millisecond)
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	appDir := filepath.Join(tmpDir, "app")
	versions := []string{filepath.Join(tmpDir, "v1"), filepath.Join(tmpDir, "v2"), filepath.Join(tmpDir, "v3")}
	for _, dir := range append([]string{authDir, appDir}, versions...) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("failed to create %s: %v", dir, err)
		}
	}
	for i, dir := range versions {
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), testConfigYAML(8001+i, authDir), 0o600); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}
	}
	configPath := filepath.Join(appDir, "config.yaml")
	if err := os.Symlink(filepath.Join(versions[0], "config.yaml"), configPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, ports := startConfigWatcher(t, configPath, authDir)

	// No watch sees a retarget; the periodic check applies it.
	for i, dir := range versions[1:] {
		retargetSymlink(t, filepath.Join(dir, "config.yaml"), configPath)
		waitForReloadedPort(t, ports, 8002+i)
	}
	if err := os.WriteFile(filepath.Join(versions[2], "config.yaml"), testConfigYAML(8010, authDir), 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	waitForReloadedPort(t, ports, 8010)
}

// A Kubernetes ConfigMap volume holds config.yaml -> ..data/config.yaml, where
// ..data links to a timestamped directory, and an update swaps ..data to a new one.
func TestWatcherAppliesAKubernetesStyleDataSwapWithinOneInterval(t *testing.T) {
	setConfigPollInterval(t, 50*time.Millisecond)
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	volume := filepath.Join(tmpDir, "volume")
	for _, dir := range []string{authDir, volume} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("failed to create %s: %v", dir, err)
		}
	}
	publish := func(name string, port int) {
		t.Helper()
		dir := filepath.Join(volume, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("failed to create %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), testConfigYAML(port, authDir), 0o600); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}
	}
	publish("..v1", 8001)
	if err := os.Symlink("..v1", filepath.Join(volume, "..data")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	configPath := filepath.Join(volume, "config.yaml")
	if err := os.Symlink(filepath.Join("..data", "config.yaml"), configPath); err != nil {
		t.Fatalf("failed to link config: %v", err)
	}
	_, ports := startConfigWatcher(t, configPath, authDir)

	for i, port := range []int{8002, 8003} {
		name := fmt.Sprintf("..v%d", i+2)
		publish(name, port)
		retargetSymlink(t, name, filepath.Join(volume, "..data"))
		// The swap is applied before the previous version is removed.
		waitForReloadedPort(t, ports, port)
		if err := os.RemoveAll(filepath.Join(volume, fmt.Sprintf("..v%d", i+1))); err != nil {
			t.Fatalf("failed to remove the previous version: %v", err)
		}
	}
}

func TestWatcherAppliesAChangeNoWatchReports(t *testing.T) {
	setConfigPollInterval(t, 50*time.Millisecond)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, testConfigYAML(8001, authDir), 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	w, ports := startConfigWatcher(t, configPath, authDir)

	// Drop every filesystem watch. This stands in for a change no event reports,
	// such as a symlink retargeted in an ancestor directory or a directory bind mount.
	for _, path := range w.watcher.WatchList() {
		// Removing a directory watch can drop the watches on its files with it.
		if err := w.watcher.Remove(path); err != nil && !errors.Is(err, fsnotify.ErrNonExistentWatch) {
			t.Fatalf("failed to remove watch %s: %v", path, err)
		}
	}
	if err := os.WriteFile(configPath, testConfigYAML(8002, authDir), 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	waitForReloadedPort(t, ports, 8002)
}

func TestWatcherPollAppliesAWriteThatLandedBeforeStart(t *testing.T) {
	setConfigPollInterval(t, 50*time.Millisecond)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, testConfigYAML(8001, authDir), 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	applied, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	// The file changes after the service loaded it and before the watcher starts.
	if err = os.WriteFile(configPath, testConfigYAML(8002, authDir), 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	ports := make(chan int, 16)
	w, err := NewWatcher(configPath, authDir, func(cfg *config.Config) { ports <- cfg.Port })
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}
	w.SetConfig(applied)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		if errStop := w.Stop(); errStop != nil {
			t.Errorf("failed to stop watcher: %v", errStop)
		}
	})
	if err = w.Start(ctx); err != nil {
		t.Fatalf("failed to start watcher: %v", err)
	}
	waitForReloadedPort(t, ports, 8002)
}

func TestPeriodicCheckSkipsBytesThatFailedToLoad(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: [\n"), 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	w := &Watcher{configPath: configPath, authDir: tmpDir}

	w.reloadConfigIfChanged()
	if w.configBytesChanged() {
		t.Fatal("bytes that just failed to load count as a change, so every check would retry them")
	}
	if err := os.WriteFile(configPath, []byte("port: ]\n"), 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	if !w.configBytesChanged() {
		t.Fatal("new bytes after a failed load do not count as a change")
	}
	// The failure may belong to bytes written between the check and the load, so the
	// same bytes are retried after a while rather than never.
	w.reloadConfigIfChanged()
	w.clientsMutex.Lock()
	w.failedConfigAt = w.failedConfigAt.Add(-failedConfigRetryInterval)
	w.clientsMutex.Unlock()
	if !w.configBytesChanged() {
		t.Fatal("failed bytes are never retried")
	}
}
