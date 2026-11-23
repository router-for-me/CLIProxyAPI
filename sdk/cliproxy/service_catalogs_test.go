package cliproxy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestServiceCatalogStartupAndConfigReload(t *testing.T) {
	fixture := func(name string) string {
		t.Helper()
		path, errAbs := filepath.Abs(filepath.Join("..", "..", "internal", "registry", "models", name))
		if errAbs != nil {
			t.Fatal(errAbs)
		}
		return path
	}
	writeCatalog := func(name string, data []byte) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), name)
		if errWrite := os.WriteFile(path, data, 0600); errWrite != nil {
			t.Fatal(errWrite)
		}
		return path
	}
	original := registry.GetDevinModelsJSON()
	originalPath := writeCatalog("original.json", original)
	first := []byte(`{"devin":[{"id":"devin/sdk-startup-regression","display_name":"SDK startup"}]}`)
	second := []byte(`{"devin":[{"id":"devin/sdk-reload-regression","display_name":"SDK reload"}]}`)
	cfg := &config.Config{Models: config.ModelCatalogs{
		Catalog:      fixture("models.json"),
		CodexCatalog: fixture("codex_client_models.json"),
		DevinCatalog: writeCatalog("first.json", first),
	}}
	s := &Service{cfg: cfg}
	ctx, cancel := context.WithCancel(context.Background())
	notifications := make(chan struct{}, 8)
	registry.SetModelRefreshCallback(func(providers []string) {
		for _, provider := range providers {
			if provider == "devin" {
				notifications <- struct{}{}
			}
		}
	})
	awaitCatalog := func(want []byte) {
		t.Helper()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case <-notifications:
				if string(registry.GetDevinModelsJSON()) == string(want) {
					return
				}
			case <-deadline.C:
				t.Fatal("service did not publish configured Devin catalog")
			}
		}
	}
	defer func() {
		cancel()
		defer registry.SetModelRefreshCallback(nil)
		restoreCtx, restoreCancel := context.WithCancel(context.Background())
		defer restoreCancel()
		restoreCfg := *cfg
		restoreCfg.Home.Enabled = false
		restoreCfg.Models = config.ModelCatalogs{
			Catalog:      fixture("models.json"),
			CodexCatalog: fixture("codex_client_models.json"),
			DevinCatalog: originalPath,
		}
		restore := &Service{cfg: &restoreCfg}
		if string(registry.GetDevinModelsJSON()) != string(original) {
			restore.startModelCatalogUpdaters(restoreCtx)
			awaitCatalog(original)
		}
	}()

	// Exercise the startup hook used by Run without starting a listening server.
	s.startModelCatalogUpdaters(ctx)
	awaitCatalog(first)
	updated := *cfg
	updated.Models.DevinCatalog = writeCatalog("second.json", second)
	if commit := s.commitConfigUpdate(&updated); commit.cfg == nil {
		t.Fatal("config update rejected")
	}
	awaitCatalog(second)

	// The same source must restart under a new service lifetime after cancellation.
	cancel()
	if errWrite := os.WriteFile(updated.Models.DevinCatalog, first, 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	restartCtx, restartCancel := context.WithCancel(context.Background())
	defer restartCancel()
	s.startModelCatalogUpdaters(restartCtx)
	awaitCatalog(first)
}
