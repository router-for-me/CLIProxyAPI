// Package routing provides adapter to integrate with existing codebase.
package routing

import (
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// Adapter bridges the new routing layer with existing auth manager.
type Adapter struct {
	router *Router
	exec   *Executor
}

// NewAdapter creates a new adapter with the given configuration and auth manager.
func NewAdapter(cfg *config.Config, authManager *coreauth.Manager) *Adapter {
	registry := NewRegistry()
	
	// TODO: Register OAuth providers from authManager
	// TODO: Register API key providers from cfg
	
	router := NewRouter(registry, cfg)
	exec := NewExecutor(router)
	
	return &Adapter{
		router: router,
		exec:   exec,
	}
}

// Router returns the underlying router.
func (a *Adapter) Router() *Router {
	return a.router
}

// Executor returns the underlying executor.
func (a *Adapter) Executor() *Executor {
	return a.exec
}
