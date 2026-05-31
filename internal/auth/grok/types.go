package grok

// PKCECodes holds a code_verifier / code_challenge pair for the OAuth2 PKCE flow.
type PKCECodes struct {
	CodeVerifier  string
	CodeChallenge string
}

// TokenResponse is the JSON body returned by xAI's token endpoints
// (authorization_code, refresh_token, and device_code grants).
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	Scope        string `json:"scope,omitempty"`
}
