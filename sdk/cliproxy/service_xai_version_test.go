package cliproxy

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestStartModelCatalogUpdatersRespectsXAIDisableVersionCheck(t *testing.T) {
	oldStart, oldStop := startXAIVersionUpdater, stopXAIVersionUpdater
	t.Cleanup(func() {
		startXAIVersionUpdater, stopXAIVersionUpdater = oldStart, oldStop
	})

	starts, stops := 0, 0
	startXAIVersionUpdater = func(context.Context, string) { starts++ }
	stopXAIVersionUpdater = func() { stops++ }

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := &config.Config{}
	cfg.XAI.DisableVersionCheck = true
	service := &Service{cfg: cfg}
	service.startModelCatalogUpdaters(ctx)
	if starts != 0 || stops != 1 {
		t.Fatalf("disabled: starts=%d stops=%d, want starts=0 stops=1", starts, stops)
	}

	cfg.XAI.DisableVersionCheck = false
	service.startModelCatalogUpdaters(ctx)
	if starts != 1 || stops != 1 {
		t.Fatalf("enabled: starts=%d stops=%d, want starts=1 stops=1", starts, stops)
	}
}
