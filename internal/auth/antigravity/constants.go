// Package antigravity provides OAuth2 authentication functionality for the Antigravity provider.
package antigravity

// OAuth client credentials and configuration
const (
	ClientID           = "884354919052-36trc1jjb3tguiac32ov6cod268c5blh.apps.googleusercontent.com"
	ClientSecret       = "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"
	LegacyClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	LegacyClientSecret = "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"
	CallbackPort       = 51121
)

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
