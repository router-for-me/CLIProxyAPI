<<<<<<< HEAD
// Package auth — OAuth token types for provider authentication.
package auth

import "time"

// Token represents an OAuth access/refresh token pair.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// OAuthProvider can refresh expired tokens.
type OAuthProvider interface {
	RefreshToken(refreshToken string) (string, error)
=======
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
>>>>>>> ci-compile-fix
}
