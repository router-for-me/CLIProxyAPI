package cliproxy

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	log "github.com/sirupsen/logrus"
)

func TestStartFileWatcherDisabled(t *testing.T) {
	factoryCalled := false
	service := &Service{
		cfg: &config.Config{DisableFileWatcher: true},
		watcherFactory: func(string, string, func(*config.Config)) (*WatcherWrapper, error) {
			factoryCalled = true
			return nil, nil
		},
	}

	if err := service.startFileWatcher(context.Background()); err != nil {
		t.Fatalf("startFileWatcher() error = %v", err)
	}
	if factoryCalled {
		t.Fatal("watcher factory was called while file watching was disabled")
	}
	if service.watcher != nil {
		t.Fatal("watcher was set while file watching was disabled")
	}
}

func TestStartFileWatcherCreationFailureIsNonFatal(t *testing.T) {
	var logs bytes.Buffer
	previousOutput := log.StandardLogger().Out
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousOutput) })

	service := &Service{
		cfg: &config.Config{},
		watcherFactory: func(string, string, func(*config.Config)) (*WatcherWrapper, error) {
			return nil, errors.New("too many open files")
		},
	}

	if err := service.startFileWatcher(context.Background()); err != nil {
		t.Fatalf("startFileWatcher() error = %v", err)
	}
	if service.watcher != nil {
		t.Fatal("watcher was set after creation failed")
	}
	if !strings.Contains(logs.String(), "continuing without hot reload") {
		t.Fatalf("expected degradation warning, got %q", logs.String())
	}
}
