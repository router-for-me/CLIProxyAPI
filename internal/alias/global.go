package alias

import (
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

var (
	globalResolver     *Resolver
	globalResolverOnce sync.Once
	globalResolverMu   sync.RWMutex
)

// GetGlobalResolver returns the global alias resolver instance.
// Creates a new empty resolver if not initialized.
func GetGlobalResolver() *Resolver {
	globalResolverOnce.Do(func() {
		globalResolver = NewResolver(nil)
	})
	globalResolverMu.RLock()
	defer globalResolverMu.RUnlock()
	return globalResolver
}

// InitGlobalResolver initializes the global resolver with configuration.
// Should be called during server startup.
func InitGlobalResolver(cfg *config.ModelAliasConfig) {
	globalResolverOnce.Do(func() {
		globalResolver = NewResolver(cfg)
	})
	globalResolverMu.Lock()
	defer globalResolverMu.Unlock()
	if globalResolver != nil && cfg != nil {
		globalResolver.Update(cfg)
	}
}

// UpdateGlobalResolver updates the global resolver configuration.
// Used for hot-reload.
func UpdateGlobalResolver(cfg *config.ModelAliasConfig) {
	r := GetGlobalResolver()
	if r != nil && cfg != nil {
		r.Update(cfg)
	}
}
