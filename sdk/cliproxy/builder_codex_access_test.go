package cliproxy

import (
	"testing"

	codexloopback "github.com/router-for-me/CLIProxyAPI/v7/internal/access/codex_loopback"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestBuilderRegistersCodexLoopbackAccessOnColdStart(t *testing.T) {
	t.Cleanup(func() {
		disabled := config.DefaultCodexIntegrationConfig()
		codexloopback.Register(&config.SDKConfig{CodexIntegration: disabled})
	})
	integration := config.DefaultCodexIntegrationConfig()
	integration.Enabled = true
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{CodexIntegration: integration},
		AuthDir:   t.TempDir(),
	}
	manager := sdkaccess.NewManager()
	_, err := NewBuilder().
		WithConfig(cfg).
		WithConfigPath(t.TempDir() + "/config.yaml").
		WithRequestAccessManager(manager).
		Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	found := false
	for _, provider := range manager.Providers() {
		if provider != nil && provider.Identifier() == codexloopback.DefaultProviderName {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("cold-start access manager is missing the Codex loopback provider")
	}
}
