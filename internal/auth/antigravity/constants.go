// Package antigravity provides OAuth2 authentication functionality for the Antigravity provider.
package antigravity

import (
	"os"
	"strings"
)

// OAuth client credentials and configuration
const (
	ClientIDEnv         = "CLIPROXY_ANTIGRAVITY_OAUTH_CLIENT_ID"
	ClientSecretEnv     = "CLIPROXY_ANTIGRAVITY_OAUTH_CLIENT_SECRET"
	DefaultClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	DefaultClientSecret = "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"
	CallbackPort        = 51121
)

var (
	ClientID     = OAuthClientID()
	ClientSecret = OAuthClientSecret()
)

func OAuthClientID() string {
	if value := strings.TrimSpace(os.Getenv(ClientIDEnv)); value != "" {
		return value
	}
	return DefaultClientID
}

func OAuthClientSecret() string {
	if value := strings.TrimSpace(os.Getenv(ClientSecretEnv)); value != "" {
		return value
	}
	return DefaultClientSecret
}

// Scopes defines the OAuth scopes required for Antigravity authentication
var Scopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

// OAuth2 endpoints for Google authentication
const (
	TokenEndpoint    = "https://oauth2.googleapis.com/token"
	AuthEndpoint     = "https://accounts.google.com/o/oauth2/v2/auth"
	UserInfoEndpoint = "https://www.googleapis.com/oauth2/v2/userinfo?alt=json"
)

// Antigravity API configuration
const (
	APIEndpoint      = "https://cloudcode-pa.googleapis.com"
	DailyAPIEndpoint = "https://daily-cloudcode-pa.googleapis.com"
	APIVersion       = "v1internal"
)
