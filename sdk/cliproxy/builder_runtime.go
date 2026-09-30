package cliproxy

import (
	configaccess "github.com/router-for-me/CLIProxyAPI/v8/internal/access/config_access"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

// WithEphemeralAPIKey registers a runtime-only API key for public API requests.
func (b *Builder) WithEphemeralAPIKey(key string) *Builder {
	b.ephemeralAPIKey = key
	return b
}

// configureRuntimeAccess keeps host credentials out of the upstream service literal.
// It runs once during Build, before the service is published to the caller.
func (s *Service) configureRuntimeAccess(key string) {
	provider := configaccess.NewRuntimeAPIKeyProvider("cliproxyapi-ephemeral", key)
	if provider == nil {
		return
	}
	s.runtimeAccessProviders = []sdkaccess.Provider{provider}
	providers := append(s.accessManager.Providers(), provider)
	s.accessManager.SetProviders(providers)
	s.serverOptions = append(s.serverOptions, api.WithRuntimeAccessProviders(provider))
}
