// Package antigravity provides OAuth2 authentication functionality for the Antigravity provider.
package antigravity

import (
	"os"
	"strings"
)

// OAuth client credentials and configuration.
// These must match the real Antigravity IDE plugin's registered OAuth client, since
// businessaicode.googleapis.com attributes API quota/enablement to the token's client
// project - a different (e.g. CLI-only) client project may not have that API enabled.
const (
	CallbackPort    = 51121
	ClientIDEnv     = "ANTIGRAVITY_CLIENT_ID"
	ClientSecretEnv = "ANTIGRAVITY_CLIENT_SECRET"
)

func OAuthClientID() string {
	return strings.TrimSpace(os.Getenv(ClientIDEnv))
}

func OAuthClientSecret() string {
	return strings.TrimSpace(os.Getenv(ClientSecretEnv))
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

// BAICLicensesEndpoint reports Gemini Enterprise (Business AI Code) licenses assigned to the
// authenticated user, including the GCP project/region generation traffic must route to.
// Individual/free accounts have no licenses and get an empty "licenses" array here.
const BAICLicensesEndpoint = "https://businessaicode.googleapis.com/v1beta:fetchLicenses"
