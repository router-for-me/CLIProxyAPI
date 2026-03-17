package codex

// PKCECodes holds the verification codes for the OAuth2 PKCE (Proof Key for Code Exchange) flow.
// PKCE is an extension to the Authorization Code flow to prevent CSRF and authorization code injection attacks.
type PKCECodes struct {
	// CodeVerifier is the cryptographically random string used to correlate
	// the authorization request to the token request
	CodeVerifier string `json:"code_verifier"`
	// CodeChallenge is the SHA256 hash of the code verifier, base64url-encoded
	CodeChallenge string `json:"code_challenge"`
}

// CodexTokenData holds the OAuth token information obtained from OpenAI.
// It includes the ID token, access token, refresh token, and associated user details.
type CodexTokenData struct {
	// IDToken is the JWT ID token containing user claims
	IDToken string `json:"id_token"`
	// AccessToken is the OAuth2 access token for API access
	AccessToken string `json:"access_token"`
	// RefreshToken is used to obtain new access tokens
	RefreshToken string `json:"refresh_token"`
	// AccountID is the OpenAI account identifier
	AccountID string `json:"account_id"`
	// Email is the OpenAI account email
	Email string `json:"email"`
	// Expire is the timestamp of the token expire
	Expire string `json:"expired"`
}

// CodexAuthBundle aggregates all authentication-related data after the OAuth flow is complete.
// This includes the API key, token data, and the timestamp of the last refresh.
type CodexAuthBundle struct {
	// APIKey is the OpenAI API key obtained from token exchange
	APIKey string `json:"api_key"`
	// TokenData contains the OAuth tokens from the authentication flow
	TokenData CodexTokenData `json:"token_data"`
	// LastRefresh is the timestamp of the last token refresh
	LastRefresh string `json:"last_refresh"`
}
