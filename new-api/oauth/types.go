package oauth

// OAuthToken represents the token received from OAuth provider
type OAuthToken struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	Scope        string `json:"scope,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	ClientID     string `json:"-"` // Expected OIDC audience from the server-owned flow.
}

// OAuthUser represents the user info from OAuth provider
type OAuthUser struct {
	// ProviderUserID is the unique identifier from the OAuth provider
	ProviderUserID string
	// Username is the username from the OAuth provider (e.g., GitHub login)
	Username string
	// DisplayName is the display name from the OAuth provider
	DisplayName string
	// Email is the email from the OAuth provider
	Email string
	// Extra contains any additional provider-specific data
	Extra map[string]any
}

// OAuthError represents a translatable OAuth error
type OAuthError struct {
	// MsgKey is the i18n message key
	MsgKey string
	// Params contains optional parameters for the message template
	Params map[string]any
	// RawError is the underlying error for logging purposes
	RawError string
}

func (e *OAuthError) Error() string {
	if e.RawError != "" {
		return e.RawError
	}
	return e.MsgKey
}

// NewOAuthError creates a new OAuth error with the given message key
func NewOAuthError(msgKey string, params map[string]any) *OAuthError {
	return &OAuthError{
		MsgKey: msgKey,
		Params: params,
	}
}

// NewOAuthErrorWithRaw creates a new OAuth error with raw error message for logging
func NewOAuthErrorWithRaw(msgKey string, params map[string]any, rawError string) *OAuthError {
	return &OAuthError{
		MsgKey:   msgKey,
		Params:   params,
		RawError: rawError,
	}
}

// AccessDeniedError is a direct user-facing access denial message.
type AccessDeniedError struct {
	Message string
}

func (e *AccessDeniedError) Error() string {
	return e.Message
}
