// Package antigravity provides OAuth2 authentication functionality for the Antigravity provider.
package antigravity

// OAuth client credentials and configuration
const (
	ClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	ClientSecret = "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"
	CallbackPort = 51121
)

// Scopes defines the OAuth scopes required for Antigravity authentication
var Scopes = []string{
	"openid",
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
	APIEndpoint             = "https://cloudcode-pa.googleapis.com"
	DailyAPIEndpoint        = "https://daily-cloudcode-pa.googleapis.com"
	DailySandboxAPIEndpoint = "https://daily-cloudcode-pa.sandbox.googleapis.com"
	APIVersion              = "v1internal"
)

// TokenRefreshSkewSeconds is the window before expiry when a token should be refreshed.
// Mirrors Rust: TOKEN_REFRESH_SKEW_SECONDS = 900
const TokenRefreshSkewSeconds = 900
