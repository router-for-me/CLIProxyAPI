// Package auth provides authentication helpers for CLIProxy.
// oauth_types.go defines types for OAuth token management.
package auth

import (
	"context"
	"time"
)

// Token holds an OAuth access/refresh token pair with an expiration time.
type Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// IsExpired returns true when the token's expiry has passed.
func (t *Token) IsExpired() bool {
	return time.Now().After(t.ExpiresAt)
}

// OAuthProvider is the interface implemented by concrete OAuth providers.
// RefreshToken exchanges a refresh token for a new access token.
type OAuthProvider interface {
	RefreshToken(ctx context.Context, refreshToken string) (string, error)
}
