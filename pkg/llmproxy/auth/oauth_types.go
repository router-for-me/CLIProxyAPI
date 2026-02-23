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
}
