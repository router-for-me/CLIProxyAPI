package copilot

// GitHub OAuth Device Flow Constants
const (
	// GitHubDeviceCodeURL is the URL for initiating the OAuth 2.0 device authorization flow.
	GitHubDeviceCodeURL = "https://github.com/login/device/code"
	// GitHubAccessTokenURL is the URL for exchanging device codes for access tokens.
	GitHubAccessTokenURL = "https://github.com/login/oauth/access_token"
	// GitHubCopilotTokenURL is the URL for exchanging GitHub tokens for Copilot tokens.
	GitHubCopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"
	// GitHubClientID is the official GitHub Copilot Client ID.
	GitHubClientID = "Iv1.b507a08c87ecfe98"
	// GitHubScope defines the permissions requested by the application.
	GitHubScope = "read:user"
)

// DeviceCodeResponse represents the response from GitHub's device authorization endpoint.
type DeviceCodeResponse struct {
	// DeviceCode is the code that the client uses to poll for an access token.
	DeviceCode string `json:"device_code"`
	// UserCode is the code that the user enters at the verification URI.
	UserCode string `json:"user_code"`
	// VerificationURI is the URL where the user can enter the user code to authorize the device.
	VerificationURI string `json:"verification_uri"`
	// ExpiresIn is the time in seconds until the device_code and user_code expire.
	ExpiresIn int `json:"expires_in"`
	// Interval is the minimum time in seconds that the client should wait between polling requests.
	Interval int `json:"interval"`
}

// GitHubTokenResponse represents the successful token response from GitHub's token endpoint.
type GitHubTokenResponse struct {
	// AccessToken is the GitHub access token (ghu_xxx).
	AccessToken string `json:"access_token"`
	// TokenType indicates the type of token, typically "bearer".
	TokenType string `json:"token_type"`
	// Scope defines the permissions granted by the token.
	Scope string `json:"scope"`
	// Error contains error code if the request failed.
	Error string `json:"error,omitempty"`
	// ErrorDescription provides human-readable error details.
	ErrorDescription string `json:"error_description,omitempty"`
}

// CopilotTokenResponse represents the response from GitHub's Copilot token exchange endpoint.
type CopilotTokenResponse struct {
	// Token is the Copilot API bearer token (tid=xxx;exp=xxx;...).
	Token string `json:"token"`
	// ExpiresAt is the Unix timestamp when the Copilot token expires.
	ExpiresAt int64 `json:"expires_at"`
	// RefreshIn is the number of seconds until refresh is recommended.
	RefreshIn int `json:"refresh_in"`
	// Endpoints contains the API endpoints for Copilot services.
	Endpoints struct {
		API       string `json:"api"`
		Proxy     string `json:"proxy"`
		Telemetry string `json:"telemetry"`
	} `json:"endpoints"`
	// SKU is the Copilot subscription type.
	SKU string `json:"sku"`
	// ChatEnabled indicates if chat functionality is enabled.
	ChatEnabled bool `json:"chat_enabled"`
	// Individual indicates if this is an individual subscription.
	Individual bool `json:"individual"`
}

// CopilotTokenData holds Copilot token information.
type CopilotTokenData struct {
	// GitHubToken is the GitHub OAuth access token.
	GitHubToken string `json:"github_token"`
	// CopilotToken is the Copilot API token.
	CopilotToken string `json:"copilot_token"`
	// CopilotAPIBase is the base URL for Copilot API.
	CopilotAPIBase string `json:"copilot_api_base"`
	// CopilotExpire is the timestamp when the Copilot token expires.
	CopilotExpire string `json:"copilot_expire"`
	// Email is the GitHub account email.
	Email string `json:"email,omitempty"`
	// SKU is the subscription type.
	SKU string `json:"sku,omitempty"`
}
