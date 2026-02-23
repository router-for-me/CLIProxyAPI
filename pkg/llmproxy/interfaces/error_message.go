// Package interfaces defines the core interfaces and shared structures for the CLI Proxy API server.
// These interfaces provide a common contract for different components of the application,
// such as AI service clients, API handlers, and data models.
package interfaces

import (
	internalinterfaces "github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
)

// ErrorMessage is an alias to the internal ErrorMessage, ensuring type compatibility
// across pkg/llmproxy/interfaces and internal/interfaces.
type ErrorMessage = internalinterfaces.ErrorMessage
