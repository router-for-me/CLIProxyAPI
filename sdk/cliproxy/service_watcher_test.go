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

func TestStartFileWatcherStartFailureIsNonFatal(t *testing.T) {
	var logs bytes.Buffer
	previousOutput := log.StandardLogger().Out
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousOutput) })

	watcherStopped := false
	service := &Service{
		cfg: &config.Config{},
		watcherFactory: func(string, string, func(*config.Config)) (*WatcherWrapper, error) {
			return &WatcherWrapper{
				start: func(context.Context) error {
					return errors.New("permission denied")
				},
				stop: func() error {
					watcherStopped = true
					return nil
				},
			}, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := service.startFileWatcher(ctx); err != nil {
		t.Fatalf("startFileWatcher() error = %v", err)
	}
	if !watcherStopped {
		t.Fatal("watcher was not stopped after startup failed")
	}
	if service.watcher != nil {
		t.Fatal("watcher was retained after startup failed")
	}
	if service.watcherCancel != nil {
		t.Fatal("watcher cancellation function was retained after startup failed")
	}
	if !strings.Contains(logs.String(), "failed to start file watcher; continuing without hot reload") {
		t.Fatalf("expected startup degradation warning, got %q", logs.String())
	}
}
