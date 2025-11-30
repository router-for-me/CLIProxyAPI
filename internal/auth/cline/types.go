/**
 * @file Core type definitions for Cline authentication
 * @description Defines data structures for Cline API authentication including token responses,
 * user information, and organization details. These types mirror the Cline API contract
 * and provide type-safe handling of authentication data throughout the system.
 */

package cline

// ClineTokenRefreshResponse represents the API response from Cline's token refresh endpoint.
// It contains the new access token, user information, and token metadata.
type ClineTokenRefreshResponse struct {
	// Success indicates whether the token refresh operation succeeded
	Success bool `json:"success"`
	// Data contains the actual token and user information
	Data ClineTokenRefreshData `json:"data"`
}

// ClineTokenRefreshData holds the token and user information returned from refresh.
type ClineTokenRefreshData struct {
	// AccessToken is the JWT token used for API authentication
	AccessToken string `json:"accessToken"`
	// RefreshToken is the token used to obtain new access tokens (may be empty if unchanged)
	RefreshToken string `json:"refreshToken,omitempty"`
	// TokenType indicates the type of token (typically "Bearer")
	TokenType string `json:"tokenType"`
	// ExpiresAt is the ISO 8601 timestamp when the access token expires
	ExpiresAt string `json:"expiresAt"`
	// UserInfo contains information about the authenticated user
	UserInfo ClineUserInfo `json:"userInfo"`
}

// ClineUserInfo represents user account information from Cline.
type ClineUserInfo struct {
	// Subject is the user's unique identifier (may be null)
	Subject *string `json:"subject"`
	// Email is the user's email address
	Email string `json:"email"`
	// Name is the user's display name
	Name string `json:"name"`
	// ClineUserID is the Cline-specific user identifier (may be null)
	ClineUserID *string `json:"clineUserId"`
	// Accounts is a list of associated account identifiers (may be null)
	Accounts []string `json:"accounts"`
}

// ClineTokenData represents the OAuth credentials for Cline authentication.
type ClineTokenData struct {
	// AccessToken is the JWT token used for API requests
	AccessToken string `json:"access_token"`
	// RefreshToken is used to obtain new access tokens when the current one expires
	RefreshToken string `json:"refresh_token"`
	// Email is the Cline account email address
	Email string `json:"email"`
	// Expire is the timestamp when the access token expires
	Expire string `json:"expired"`
}
