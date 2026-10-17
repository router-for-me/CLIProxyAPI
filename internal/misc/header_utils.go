// Package misc provides miscellaneous utility functions for the CLI Proxy API server.
// It includes helper functions for HTTP header manipulation and other common operations
// that don't fit into more specific packages.
package misc

import (
	"net/http"
	"strings"
)

// EnsureHeader ensures that a header exists in the target header map by checking
// multiple sources in order of priority: source headers, existing target headers,
// and finally the default value. It only sets the header if it's not already present
// and the value is not empty after trimming whitespace.
//
// Parameters:
//   - target: The target header map to modify
//   - source: The source header map to check first (can be nil)
//   - key: The header key to ensure
//   - defaultValue: The default value to use if no other source provides a value
func EnsureHeader(target http.Header, source http.Header, key, defaultValue string) {
	if target == nil {
		return
	}
	if source != nil {
		if val := strings.TrimSpace(source.Get(key)); val != "" {
			target.Set(key, val)
			return
		}
	}
	if strings.TrimSpace(target.Get(key)) != "" {
		return
	}
	if val := strings.TrimSpace(defaultValue); val != "" {
		target.Set(key, val)
	}
}
