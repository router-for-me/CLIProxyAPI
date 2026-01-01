package kiro

import "time"

// KiroCredentials represents the authentication tokens for Kiro (Amazon Q).
// It stores the access and refresh tokens along with expiration info and region configuration.
type KiroCredentials struct {
	Type         string    `json:"type"`                    // Provider type identifier for watcher detection
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	TokenType    string    `json:"token_type"`
	ExpiresIn    int       `json:"expires_in"`
	Region       string    `json:"region"`
	StartUrl     string    `json:"start_url,omitempty"`
	AuthMethod   string    `json:"auth_method,omitempty"`   // "social" for Google/etc, "iam" for IAM/SSO
	ProfileArn   string    `json:"profile_arn,omitempty"`   // CodeWhisperer profile ARN
	Provider     string    `json:"provider,omitempty"`      // Auth provider (e.g., "Google")
	ClientID     string    `json:"client_id,omitempty"`     // For IAM/SSO auth
	ClientSecret string    `json:"client_secret,omitempty"` // For IAM/SSO auth
}

// TokenResponse represents the response from the OIDC token endpoint.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}
