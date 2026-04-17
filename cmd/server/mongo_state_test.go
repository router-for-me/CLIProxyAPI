package main

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/store/mongostate"
)

func TestInitRuntimeStateManager_FailsWhenURIIsMissing(t *testing.T) {
	runtimeCfg := mongostate.RuntimeConfig{Enabled: true}
	runtimeCfg.Normalize()

	mgr, err := initRuntimeStateManager(context.Background(), runtimeCfg)
	if err == nil {
		t.Fatal("expected error when mongo-state uri is missing")
	}
	if mgr != nil {
		t.Fatal("expected nil manager when mongo-state uri is missing")
	}
}
