package access

import (
	"testing"

	homeaccess "github.com/router-for-me/CLIProxyAPI/v7/internal/access/home_access"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

func TestRegisterBuiltInProvidersHomeEnabledWithEmptyAPIKeys(t *testing.T) {
	sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypeConfigAPIKey)
	homeaccess.Unregister()
	t.Cleanup(func() {
		sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypeConfigAPIKey)
		homeaccess.Unregister()
	})

	RegisterBuiltInProviders(&config.Config{Home: config.HomeConfig{Enabled: true}})

	providers := sdkaccess.RegisteredProviders()
	if len(providers) != 1 {
		t.Fatalf("RegisteredProviders() len = %d, want 1", len(providers))
	}
	if providers[0].Identifier() != homeaccess.ProviderTypeHome {
		t.Fatalf("RegisteredProviders()[0] = %q, want %q", providers[0].Identifier(), homeaccess.ProviderTypeHome)
	}
}
