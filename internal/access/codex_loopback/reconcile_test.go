package codexloopback

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

func TestRegisterFollowsCodexIntegrationState(t *testing.T) {
	t.Cleanup(func() { sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypeCodexLoopback) })
	cfg := config.DefaultCodexIntegrationConfig()
	Register(&config.SDKConfig{CodexIntegration: cfg})
	if hasRegisteredLoopbackProvider() {
		t.Fatal("disabled integration registered loopback provider")
	}

	cfg.Enabled = true
	Register(&config.SDKConfig{CodexIntegration: cfg})
	if !hasRegisteredLoopbackProvider() {
		t.Fatal("enabled integration did not register loopback provider")
	}

	cfg.LoopbackAccess = false
	Register(&config.SDKConfig{CodexIntegration: cfg})
	if hasRegisteredLoopbackProvider() {
		t.Fatal("disabled loopback access left provider registered")
	}
}

func hasRegisteredLoopbackProvider() bool {
	for _, provider := range sdkaccess.RegisteredProviders() {
		if provider.Identifier() == DefaultProviderName {
			return true
		}
	}
	return false
}
