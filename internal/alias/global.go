package alias

import (
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

var (
	globalResolver     *Resolver
	globalResolverOnce sync.Once
)

// GetGlobalResolver returns the global alias resolver instance.
// Creates a new empty resolver if not initialized.
func GetGlobalResolver() *Resolver {
	globalResolverOnce.Do(func() {
		globalResolver = NewResolver(nil)
	})
	return globalResolver
}

// InitGlobalResolver initializes the global resolver with configuration.
// Should be called during server startup.
func InitGlobalResolver(cfg *config.ModelAliasConfig) {
	GetGlobalResolver().Update(cfg)
}

// UpdateGlobalResolver updates the global resolver configuration.
// Used for hot-reload.
func UpdateGlobalResolver(cfg *config.ModelAliasConfig) {
	GetGlobalResolver().Update(cfg)
}
