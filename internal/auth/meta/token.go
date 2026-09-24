// Package meta provides OAuth2 authentication helpers for Meta.
package meta

import "time"

const (
	// DefaultAPIBaseURL is the default Meta API base URL.
	DefaultAPIBaseURL = "https://api.meta.com"
	// Issuer is Meta's OAuth issuer.
	Issuer = "https://auth.meta.com"
	// DiscoveryURL is the OIDC discovery endpoint to resolve OAuth endpoints.
	DiscoveryURL = Issuer + "/.well-known/openid-configuration"
	// ClientID is the public Meta OAuth client ID (to be updated once Meta provides it).
	ClientID = "1234567890"
	// Scope is the OAuth scope set required for Meta API access.
	Scope = "openid profile email offline_access"
	// DeviceCodeGrantType is the OAuth2 device authorization grant type (RFC 8628).
	DeviceCodeGrantType = "urn:ietf:params:oauth:grant-type:device_code"
	// defaultPollInterval is used when the device endpoint does not specify an interval.
	defaultPollInterval = 5 * time.Second
	// httpClientTimeout bounds credential acquisition HTTP calls (device/token/refresh).
	httpClientTimeout = 30 * time.Second
	// MaxPollDuration is the upper limit for waiting on user authorization.
	MaxPollDuration = 30 * time.Minute
)

var refreshLead = 5 * time.Minute

// RefreshLead returns the refresh lead time for Meta OAuth credentials.
func RefreshLead() time.Duration {
	return refreshLead
}

// Discovery contains the OAuth endpoints resolved from Meta OIDC discovery.
type Discovery struct {
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
	TokenEndpoint               string `json:"token_endpoint"`
}

// DeviceCodeResponse represents Meta's device authorization response.
type DeviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
	TokenEndpoint           string `json:"-"`
}

// TokenData holds Meta OAuth token data.
type TokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	Expire       string `json:"expired,omitempty"`
	Email        string `json:"email,omitempty"`
	Subject      string `json:"sub,omitempty"`
}

// AuthBundle aggregates token data and OAuth metadata for persistence.
type AuthBundle struct {
	TokenData     TokenData
	LastRefresh   string
	BaseURL       string
	RedirectURI   string
	TokenEndpoint string
}
