package cliproxy

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestStartModelCatalogUpdatersRespectsXAIDisableVersionCheck(t *testing.T) {
	oldStartXAIVersionUpdater := startXAIVersionUpdater
	t.Cleanup(func() {
		startXAIVersionUpdater = oldStartXAIVersionUpdater
	})

	calls := 0
	startXAIVersionUpdater = func(context.Context, string) {
		calls++
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := &config.Config{}
	cfg.XAI.DisableVersionCheck = true
	service := &Service{cfg: cfg}
	service.startModelCatalogUpdaters(ctx)
	if calls != 0 {
		t.Fatalf("xai version updater calls = %d, want 0 when disabled", calls)
	}

	cfg.XAI.DisableVersionCheck = false
	service.startModelCatalogUpdaters(ctx)
	if calls != 1 {
		t.Fatalf("xai version updater calls = %d, want 1 when enabled", calls)
	}
}
