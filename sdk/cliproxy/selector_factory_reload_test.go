package cliproxy

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// A file reload the selector factory rejects must not be adopted by the watcher:
// otherwise its recorded hash skips the retry once the factory accepts the file.
func TestSelectorFactoryRejectsWatcherReloadBeforeAdoption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	authDir := filepath.Join(dir, "auth")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var rejectFillFirst atomic.Bool
	service, running := factoryErrorService(func(*config.Config) (coreauth.Selector, error) {
		if rejectFillFirst.Load() {
			return nil, errors.New("fill-first policy unavailable")
		}
		return &factoryOwnedSelector{}, nil
	})
	wrapper, err := defaultWatcherFactory(path, authDir, service.applyWatcherConfigUpdate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := wrapper.Stop(); err != nil {
			t.Errorf("stop watcher fixture: %v", err)
		}
	})
	// Run installs the same check on the watcher it creates.
	wrapper.SetConfigValidator(service.checkRoutingSelector)

	rejectFillFirst.Store(true)
	rejected := []byte("auth-dir: " + authDir + "\nrouting: {strategy: fill-first}\n")
	if err := os.WriteFile(path, rejected, 0o600); err != nil {
		t.Fatal(err)
	}
	wrapper.ReloadConfigIfChanged()
	if got := service.currentConfig().Routing.Strategy; got == "fill-first" {
		t.Fatal("service published a config its selector factory rejected")
	}
	if service.coreManager.Selector() != running || running.stops.Load() != 0 {
		t.Fatal("rejected reload replaced or stopped the running selector")
	}
	if onDisk, errRead := os.ReadFile(path); errRead != nil || string(onDisk) != string(rejected) {
		t.Fatalf("rejected reload rewrote the config file: %q %v", onDisk, errRead)
	}

	// Same bytes, factory now accepts: a watcher that adopted the rejected file skips this.
	rejectFillFirst.Store(false)
	wrapper.ReloadConfigIfChanged()
	if got := service.currentConfig().Routing.Strategy; got != "fill-first" {
		t.Fatalf("watcher adopted the rejected file and skipped its retry: strategy %q", got)
	}
	if service.coreManager.Selector() == running || running.stops.Load() != 1 {
		t.Fatal("accepted retry did not replace the previous selector")
	}
}
