// Package empty provides a no-operation token storage implementation.
// This package is used when authentication tokens are not required or when
// using API key-based authentication instead of OAuth tokens for any provider.
package empty

// EmptyStorage is a no-operation implementation of the TokenStorage interface.
// It provides empty implementations for scenarios where token storage is not needed,
// such as when using API keys instead of OAuth tokens for authentication.
type EmptyStorage struct {
	// Type indicates the authentication provider type, always "empty" for this implementation.
	Type string `json:"type"`
}

// SaveTokenToFile is a no-operation implementation that always succeeds.
// This method satisfies the TokenStorage interface but performs no actual file operations
// since empty storage doesn't require persistent token data.
//
// Parameters:
//   - _: The file path parameter is ignored in this implementation
//
// Returns:
//   - error: Always returns nil (no error)
func (ts *EmptyStorage) SaveTokenToFile(_ string) error {
	ts.Type = "empty"
	return nil
}
