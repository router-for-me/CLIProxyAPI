// Package api provides type aliases to the internal implementation.
// This allows both "internal/api" and "pkg/llmproxy/api" import paths to work seamlessly.
package api

import "github.com/router-for-me/CLIProxyAPI/v6/internal/api"

// Type aliases
type ServerOption = api.ServerOption
type Server = api.Server

// Function aliases for exported API functions
var (
	WithMiddleware                = api.WithMiddleware
	WithEngineConfigurator        = api.WithEngineConfigurator
	WithLocalManagementPassword   = api.WithLocalManagementPassword
	WithKeepAliveEndpoint         = api.WithKeepAliveEndpoint
	WithRequestLoggerFactory      = api.WithRequestLoggerFactory
	NewServer                     = api.NewServer
)
