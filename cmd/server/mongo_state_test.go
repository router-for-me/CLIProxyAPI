package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	log "github.com/sirupsen/logrus"
)

func TestInitRuntimeStateManager_DisablesPersistenceWhenURIIsMissing(t *testing.T) {
	cfg := &config.Config{}
	cfg.MongoState.Enabled = true

	originalOutput := log.StandardLogger().Out
	defer log.SetOutput(originalOutput)

	var buf bytes.Buffer
	log.SetOutput(&buf)

	mgr := initRuntimeStateManager(context.Background(), cfg)
	if mgr != nil {
		t.Fatal("expected nil manager when mongo-state uri is missing")
	}
	if cfg.MongoState.Enabled {
		t.Fatal("expected mongo-state to be disabled after downgrade")
	}
	if !strings.Contains(buf.String(), "mongostate: enabled but uri is empty") {
		t.Fatalf("expected downgrade warning in logs, got %q", buf.String())
	}
}
