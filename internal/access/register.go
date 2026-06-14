package access

import (
	"strings"

	configaccess "github.com/router-for-me/CLIProxyAPI/v7/internal/access/config_access"
	homeaccess "github.com/router-for-me/CLIProxyAPI/v7/internal/access/home_access"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

// RegisterBuiltInProviders updates built-in access providers for the active config.
func RegisterBuiltInProviders(cfg *config.Config) {
	if cfg == nil {
		configaccess.Register(nil)
		homeaccess.Unregister()
		sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypeAnonymous)
		return
	}
	configaccess.Register(&cfg.SDKConfig)
	if cfg.Home.Enabled {
		sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypeAnonymous)
		homeaccess.Register()
		return
	}
	homeaccess.Unregister()
	if !hasConfiguredAPIKeys(cfg) {
		sdkaccess.RegisterProvider(sdkaccess.AccessProviderTypeAnonymous, sdkaccess.NewAnonymousProvider(sdkaccess.DefaultAnonymousProviderName))
		return
	}
	sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypeAnonymous)
}

func hasConfiguredAPIKeys(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	for _, key := range cfg.SDKConfig.APIKeys {
		if strings.TrimSpace(key) != "" {
			return true
		}
	}
	return false
}
