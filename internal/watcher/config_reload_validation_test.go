package watcher

import (
	"errors"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestReloadConfigIfChangedRejectsInvalidConfigBeforeAdoption(t *testing.T) {
	configPath, authDir := newIssue6631ConfigFile(t)
	if errWrite := writeIssue6631Config(configPath, authDir, 1); errWrite != nil {
		t.Fatal(errWrite)
	}
	var applied []int
	w := &Watcher{
		configPath:     configPath,
		authDir:        authDir,
		reloadCallback: func(cfg *config.Config) { applied = append(applied, cfg.RequestRetry) },
	}
	limit := 3
	w.SetConfigValidator(func(cfg *config.Config) error {
		if cfg.RequestRetry > limit {
			return errors.New("request-retry above application limit")
		}
		return nil
	})
	w.ReloadConfigIfChanged()
	if errWrite := writeIssue6631Config(configPath, authDir, 9); errWrite != nil {
		t.Fatal(errWrite)
	}
	w.ReloadConfigIfChanged()
	if want := []int{1}; !reflect.DeepEqual(applied, want) {
		t.Fatalf("applied request-retry values = %v, want %v", applied, want)
	}
	if w.config == nil || w.config.RequestRetry != 1 {
		t.Fatalf("watcher adopted rejected config: %+v", w.config)
	}
	// A rejected file is not recorded as applied, so the same bytes retry once accepted.
	limit = 10
	w.ReloadConfigIfChanged()
	if want := []int{1, 9}; !reflect.DeepEqual(applied, want) {
		t.Fatalf("applied request-retry values = %v, want %v", applied, want)
	}
}
