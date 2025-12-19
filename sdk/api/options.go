// Package api provides SDK type aliases for API server configuration.
// This package re-exports types from internal/api, allowing SDK consumers
// to use server options without directly importing internal packages.
package api

import (
	"github.com/gin-gonic/gin"
	internalapi "github.com/router-for-me/CLIProxyAPI/v6/internal/api"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
)

// ServerOption customises HTTP server construction.
// This is a type alias to internal/api.ServerOption.
type ServerOption = internalapi.ServerOption

// WithMiddleware appends additional Gin middleware during server construction.
// This delegates to internal/api.WithMiddleware.
func WithMiddleware(mw ...gin.HandlerFunc) ServerOption {
	return internalapi.WithMiddleware(mw...)
}

// WithEngineConfigurator allows callers to mutate the Gin engine prior to middleware setup.
// This delegates to internal/api.WithEngineConfigurator.
func WithEngineConfigurator(fn func(*gin.Engine)) ServerOption {
	return internalapi.WithEngineConfigurator(fn)
}

// WithLocalManagementPassword stores a runtime-only management password accepted for localhost requests.
// This delegates to internal/api.WithLocalManagementPassword.
func WithLocalManagementPassword(password string) ServerOption {
	return internalapi.WithLocalManagementPassword(password)
}

// WithRequestLoggerFactory customises request logger creation.
// This delegates to internal/api.WithRequestLoggerFactory.
func WithRequestLoggerFactory(factory func(*config.Config, string) logging.RequestLogger) ServerOption {
	return internalapi.WithRequestLoggerFactory(factory)
}
