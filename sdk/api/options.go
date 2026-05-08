// Package api exposes server option helpers for embedding CLIProxyAPI.
//
// It wraps internal server option types so external projects can configure the embedded
// HTTP server without importing internal packages.
package api

import (
	"time"

	"github.com/gin-gonic/gin"
	internalapi "github.com/router-for-me/CLIProxyAPI/v6/internal/api"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/logging"
)

// ServerOption customises HTTP server construction.
type ServerOption = internalapi.ServerOption

// WithMiddleware appends additional Gin middleware during server construction.
func WithMiddleware(mw ...gin.HandlerFunc) ServerOption { return internalapi.WithMiddleware(mw...) }

// WithEngineConfigurator allows callers to mutate the Gin engine prior to middleware setup.
func WithEngineConfigurator(fn func(*gin.Engine)) ServerOption {
	return internalapi.WithEngineConfigurator(fn)
}

// WithRouterConfigurator appends a callback after default routes are registered.
func WithRouterConfigurator(fn func(*gin.Engine, *handlers.BaseAPIHandler, *config.Config)) ServerOption {
	return internalapi.WithRouterConfigurator(fn)
}

// WithLocalManagementPassword stores a runtime-only management password accepted for localhost requests.
func WithLocalManagementPassword(password string) ServerOption {
	return internalapi.WithLocalManagementPassword(password)
}

// WithKeepAliveEndpoint enables a keep-alive endpoint with the provided timeout and callback.
func WithKeepAliveEndpoint(timeout time.Duration, onTimeout func()) ServerOption {
	return internalapi.WithKeepAliveEndpoint(timeout, onTimeout)
}

// WithRequestLoggerFactory customises request logger creation.
func WithRequestLoggerFactory(factory func(*config.Config, string) logging.RequestLogger) ServerOption {
	return internalapi.WithRequestLoggerFactory(factory)
}
