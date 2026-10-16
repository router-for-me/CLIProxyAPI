package watcher

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func newIssue6631ConfigFile(t *testing.T) (configPath, authDir string) {
	t.Helper()
	tmpDir := t.TempDir()
	authDir = filepath.Join(tmpDir, "auth")
	if errMkdir := os.MkdirAll(authDir, 0o755); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	return filepath.Join(tmpDir, "config.yaml"), authDir
}

// writeIssue6631Config returns an error instead of failing the test so reload
// callbacks running on other goroutines can use it.
func writeIssue6631Config(configPath, authDir string, requestRetry int) error {
	raw := fmt.Sprintf("auth-dir: %q\nrequest-retry: %d\n", filepath.ToSlash(authDir), requestRetry)
	return os.WriteFile(configPath, []byte(raw), 0o600)
}

func TestReloadConfigIfChangedReloadsWriteLandingDuringReload_Issue6631(t *testing.T) {
	configPath, authDir := newIssue6631ConfigFile(t)
	if errWrite := writeIssue6631Config(configPath, authDir, 1); errWrite != nil {
		t.Fatal(errWrite)
	}

	var applied []int
	var errSave error
	w := &Watcher{
		configPath: configPath,
		authDir:    authDir,
		reloadCallback: func(cfg *config.Config) {
			applied = append(applied, cfg.RequestRetry)
			if len(applied) == 1 {
				// A management save lands while this reload is still applying.
				errSave = writeIssue6631Config(configPath, authDir, 2)
			}
		},
	}

	w.ReloadConfigIfChanged()
	if errSave != nil {
		t.Fatal(errSave)
	}
	// The reload requested by that save must apply it.
	w.ReloadConfigIfChanged()

	if want := []int{1, 2}; !reflect.DeepEqual(applied, want) {
		t.Fatalf("applied request-retry values = %v, want %v", applied, want)
	}
}

func TestReloadConfigIfChangedSerializesConcurrentReloads_Issue6631(t *testing.T) {
	configPath, authDir := newIssue6631ConfigFile(t)
	if errWrite := writeIssue6631Config(configPath, authDir, 1); errWrite != nil {
		t.Fatal(errWrite)
	}

	var mu sync.Mutex
	var applied []int
	active, maxActive := 0, 0
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	w := &Watcher{
		configPath: configPath,
		authDir:    authDir,
		reloadCallback: func(cfg *config.Config) {
			mu.Lock()
			applied = append(applied, cfg.RequestRetry)
			call := len(applied)
			active++
			if active > maxActive {
				maxActive = active
			}
			mu.Unlock()
			switch call {
			case 1:
				close(firstEntered)
				<-releaseFirst
			case 2:
				close(secondEntered)
			}
			mu.Lock()
			active--
			mu.Unlock()
		},
	}
	release := sync.OnceFunc(func() { close(releaseFirst) })
	t.Cleanup(release)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.ReloadConfigIfChanged()
	}()
	select {
	case <-firstEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("first reload did not reach the reload callback")
	}

	// A newer save lands and requests its own reload while the first one is applying.
	if errWrite := writeIssue6631Config(configPath, authDir, 2); errWrite != nil {
		t.Fatal(errWrite)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.ReloadConfigIfChanged()
	}()
	// The bounded wait only gives an unserialized second reload the chance to
	// overlap; a serialized reload stays blocked, so this cannot fail it.
	select {
	case <-secondEntered:
	case <-time.After(200 * time.Millisecond):
	}
	release()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if maxActive != 1 {
		t.Fatalf("config reloads overlapped: %d applied at the same time", maxActive)
	}
	if want := []int{1, 2}; !reflect.DeepEqual(applied, want) {
		t.Fatalf("applied request-retry values = %v, want %v", applied, want)
	}
}

func TestReloadConfigIfChangedReloadsAfterWriteBetweenHashAndLoad_Issue6631(t *testing.T) {
	configPath, authDir := newIssue6631ConfigFile(t)
	if errWrite := writeIssue6631Config(configPath, authDir, 1); errWrite != nil {
		t.Fatal(errWrite)
	}

	originalLoad := loadConfigFileFunc
	t.Cleanup(func() { loadConfigFileFunc = originalLoad })
	var errSave error
	loads := 0
	loadConfigFileFunc = func(path string) (*config.Config, error) {
		loads++
		if loads == 1 {
			// A management save lands after the change check read version 1.
			errSave = writeIssue6631Config(configPath, authDir, 2)
		}
		return originalLoad(path)
	}

	var applied []int
	w := &Watcher{
		configPath:     configPath,
		authDir:        authDir,
		reloadCallback: func(cfg *config.Config) { applied = append(applied, cfg.RequestRetry) },
	}

	w.ReloadConfigIfChanged()
	if errSave != nil {
		t.Fatal(errSave)
	}
	// A later save restores the bytes of version 1, which were never applied.
	if errWrite := writeIssue6631Config(configPath, authDir, 1); errWrite != nil {
		t.Fatal(errWrite)
	}
	w.ReloadConfigIfChanged()

	if want := []int{2, 1}; !reflect.DeepEqual(applied, want) {
		t.Fatalf("applied request-retry values = %v, want %v", applied, want)
	}
}
