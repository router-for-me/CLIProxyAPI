// Package interfaces defines the core interfaces and shared structures for the CLI Proxy API server.
// These interfaces provide a common contract for different components of the application,
// such as AI service clients, API handlers, and data models.
package interfaces

import "net/http"

// ErrorMessage encapsulates an error with an associated HTTP status code.
// This structure is used to provide detailed error information including
// both the HTTP status and the underlying error.
type ErrorMessage struct {
	// StatusCode is the HTTP status code returned by the API.
	StatusCode int

	// Error is the underlying error that occurred.
	Error error

	// Addon contains additional headers to be added to the response.
	Addon http.Header
}
