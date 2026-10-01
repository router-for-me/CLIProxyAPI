package litellmaccess

import (
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

// Register installs (or removes) the on-the-fly LiteLLM access provider. When
// any required dependency is nil the provider is unregistered so requests fall
// through to the remaining providers. Call once during bootstrap, after the PG
// stores and LiteLLM sync store exist.
func Register(pg PGLookup, lite LiteLLMLookup, logs LogStore, settings SettingsFunc) {
	if pg == nil || lite == nil || settings == nil {
		sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypeLiteLLMAPIKey)
		return
	}
	provider := New(pg, lite, logs, settings, nil)
	if provider == nil {
		sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypeLiteLLMAPIKey)
		return
	}
	sdkaccess.RegisterProvider(sdkaccess.AccessProviderTypeLiteLLMAPIKey, provider)
}
