// Package antigravity provides OAuth2 authentication functionality for the Antigravity provider.
package antigravity

// OAuth client credentials and configuration
const (
	ClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	ClientSecret = "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"
	// CallbackPort 0 asks the OS for a random loopback port, matching the
	// official client. The legacy fixed 51121 remains supported through
	// LoginOptions.CallbackPort.
	CallbackPort = 0
	CallbackPath = "/auth/callback"
)

// Scopes defines the OAuth scopes required for Antigravity authentication.
// The order matches the official client byte-for-byte.
var Scopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
	"https://www.googleapis.com/auth/aicode",
}

// OAuth2 endpoints for Google authentication
const (
	TokenEndpoint    = "https://oauth2.googleapis.com/token"
	AuthEndpoint     = "https://accounts.google.com/o/oauth2/auth"
	UserInfoEndpoint = "https://www.googleapis.com/oauth2/v2/userinfo"
)

// Antigravity API configuration
const (
	APIEndpoint      = "https://cloudcode-pa.googleapis.com"
	DailyAPIEndpoint = "https://daily-cloudcode-pa.googleapis.com"
	APIVersion       = "v1internal"
)
