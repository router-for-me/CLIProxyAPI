package oauthflow

import (
	"context"
	"errors"
)

// ErrRefreshNotSupported is returned when a provider does not support token refresh.
var ErrRefreshNotSupported = errors.New("oauthflow: refresh not supported")

// ErrRevokeNotSupported is returned when a provider does not support token revocation.
var ErrRevokeNotSupported = errors.New("oauthflow: revoke not supported")

// TokenResult is a provider-agnostic OAuth token payload.
type TokenResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    string // RFC3339 when available
	TokenType    string
	IDToken      string
	Metadata     map[string]any
}

// OAuthSession contains state and PKCE values for browser-based OAuth flows.
type OAuthSession struct {
	State         string
	RedirectURI   string
	CodeVerifier  string
	CodeChallenge string
}

// DeviceCodeResult captures a device-code authorization response.
type DeviceCodeResult struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresIn               int
	Interval                int
	CodeVerifier            string
}

// ProviderOAuth describes a browser-based (authorization-code) OAuth provider.
type ProviderOAuth interface {
	Provider() string
	AuthorizeURL(session OAuthSession) (authURL string, updated OAuthSession, err error)
	ExchangeCode(ctx context.Context, session OAuthSession, code string) (*TokenResult, error)
	Refresh(ctx context.Context, refreshToken string) (*TokenResult, error)
	// Revoke invalidates the given token (access or refresh) at the provider.
	// Returns ErrRevokeNotSupported if the provider does not support revocation.
	Revoke(ctx context.Context, token string) error
}

// ProviderDeviceOAuth describes a device-code OAuth provider.
type ProviderDeviceOAuth interface {
	Provider() string
	DeviceAuthorize(ctx context.Context) (*DeviceCodeResult, error)
	DevicePoll(ctx context.Context, device *DeviceCodeResult) (*TokenResult, error)
	Refresh(ctx context.Context, refreshToken string) (*TokenResult, error)
	// Revoke invalidates the given token (access or refresh) at the provider.
	// Returns ErrRevokeNotSupported if the provider does not support revocation.
	Revoke(ctx context.Context, token string) error
}
