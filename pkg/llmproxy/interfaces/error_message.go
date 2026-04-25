package interfaces

import "net/http"

// ErrorMessage encapsulates an error with an associated HTTP status code.
type ErrorMessage struct {
	// StatusCode is the HTTP status code returned by the API.
	StatusCode int

	// Error is the underlying error that occurred.
	Error error

	// Addon contains additional headers to be added to the response.
	Addon http.Header
}
