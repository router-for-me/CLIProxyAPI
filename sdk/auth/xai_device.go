package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func (a XAIAuthenticator) xaiAuth(cfg *config.Config) *xaiauth.XAIAuth {
	svc := xaiauth.NewXAIAuth(cfg)
	if a.httpClient != nil {
		svc.SetHTTPClient(a.httpClient)
	}
	return svc
}

func (a XAIAuthenticator) effectiveInterval(d time.Duration) time.Duration {
	if a.pollIntervalOverride > 0 {
		return a.pollIntervalOverride
	}
	if d < 5*time.Second {
		return 5 * time.Second
	}
	return d
}

// StartDeviceFlow requests an xAI device code and returns it without waiting
// for the user to authorize.
func (a XAIAuthenticator) StartDeviceFlow(ctx context.Context, cfg *config.Config) (*DeviceFlow, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	deviceCode, err := a.xaiAuth(cfg).StartDeviceFlow(ctx)
	if err != nil {
		return nil, fmt.Errorf("xai: failed to start device flow: %w", err)
	}
	if deviceCode == nil || strings.TrimSpace(deviceCode.DeviceCode) == "" {
		return nil, fmt.Errorf("xai: failed to start device flow: empty device code")
	}
	flow := &DeviceFlow{
		Provider:                a.Provider(),
		UserCode:                strings.TrimSpace(deviceCode.UserCode),
		DeviceCode:              strings.TrimSpace(deviceCode.DeviceCode),
		VerificationURI:         strings.TrimSpace(deviceCode.VerificationURI),
		VerificationURIComplete: strings.TrimSpace(deviceCode.VerificationURIComplete),
		TokenEndpoint:           strings.TrimSpace(deviceCode.TokenEndpoint),
	}
	if deviceCode.ExpiresIn > 0 {
		flow.ExpiresIn = time.Duration(deviceCode.ExpiresIn) * time.Second
	}
	if deviceCode.Interval > 0 {
		flow.Interval = time.Duration(deviceCode.Interval) * time.Second
	}
	return flow, nil
}

// PollDeviceFlow exchanges an xAI device code once.
// A pending result means the user has not approved yet.
func (a XAIAuthenticator) PollDeviceFlow(ctx context.Context, cfg *config.Config, flow *DeviceFlow) (*DeviceFlowPoll, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if flow == nil || strings.TrimSpace(flow.DeviceCode) == "" {
		return nil, fmt.Errorf("xai device flow is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	interval := a.effectiveInterval(flow.Interval)
	svc := a.xaiAuth(cfg)
	token, next, err := svc.PollDeviceCodeOnce(ctx, &xaiauth.DeviceCodeResponse{
		DeviceCode:              flow.DeviceCode,
		UserCode:                flow.UserCode,
		VerificationURI:         flow.VerificationURI,
		VerificationURIComplete: flow.VerificationURIComplete,
		ExpiresIn:               int(flow.ExpiresIn / time.Second),
		Interval:                int(interval / time.Second),
		TokenEndpoint:           flow.TokenEndpoint,
	}, interval)
	if err != nil {
		return nil, err
	}
	if token == nil {
		return &DeviceFlowPoll{Pending: true, Interval: a.effectiveInterval(next)}, nil
	}
	record, errRecord := a.authFromXAIBundle(svc, &xaiauth.AuthBundle{
		TokenData:     *token,
		LastRefresh:   time.Now().UTC().Format(time.RFC3339),
		BaseURL:       xaiauth.DefaultAPIBaseURL,
		TokenEndpoint: strings.TrimSpace(flow.TokenEndpoint),
	})
	if errRecord != nil {
		return nil, errRecord
	}
	return &DeviceFlowPoll{Interval: a.effectiveInterval(next), Auth: record}, nil
}

// CompleteDeviceFlow polls until the xAI device code is authorized or the flow fails.
func (a XAIAuthenticator) CompleteDeviceFlow(ctx context.Context, cfg *config.Config, flow *DeviceFlow) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if flow == nil || strings.TrimSpace(flow.DeviceCode) == "" {
		return nil, fmt.Errorf("xai device flow is required")
	}
	interval := a.effectiveInterval(flow.Interval)
	deadline := time.Now().Add(xaiauth.MaxPollDuration)
	if flow.ExpiresIn > 0 {
		expiresAt := time.Now().Add(flow.ExpiresIn)
		if expiresAt.Before(deadline) {
			deadline = expiresAt
		}
	}
	return pollUntilDeviceAuthorized(ctx, interval, deadline, fmt.Errorf("xai device code expired"), func() (*DeviceFlowPoll, error) {
		polled, err := a.PollDeviceFlow(ctx, cfg, flow)
		if err != nil || polled == nil || polled.Auth != nil {
			return polled, err
		}
		polled.Interval = a.effectiveInterval(polled.Interval)
		flow.Interval = polled.Interval
		return polled, nil
	})
}

func (a XAIAuthenticator) authFromXAIBundle(authSvc *xaiauth.XAIAuth, bundle *xaiauth.AuthBundle) (*coreauth.Auth, error) {
	tokenStorage := authSvc.CreateTokenStorage(bundle)
	if tokenStorage == nil || strings.TrimSpace(tokenStorage.AccessToken) == "" {
		return nil, fmt.Errorf("xai token storage missing access token")
	}

	fileName := xaiauth.CredentialFileName(tokenStorage.Email, tokenStorage.Subject)
	label := strings.TrimSpace(tokenStorage.Email)
	if label == "" {
		label = "xAI"
	}

	metadata := map[string]any{
		"type":           "xai",
		"access_token":   tokenStorage.AccessToken,
		"refresh_token":  tokenStorage.RefreshToken,
		"id_token":       tokenStorage.IDToken,
		"token_type":     tokenStorage.TokenType,
		"expires_in":     tokenStorage.ExpiresIn,
		"expired":        tokenStorage.Expire,
		"last_refresh":   tokenStorage.LastRefresh,
		"base_url":       tokenStorage.BaseURL,
		"token_endpoint": tokenStorage.TokenEndpoint,
		"auth_kind":      "oauth",
	}
	if tokenStorage.Email != "" {
		metadata["email"] = tokenStorage.Email
	}
	if tokenStorage.Subject != "" {
		metadata["sub"] = tokenStorage.Subject
	}

	fmt.Println("xAI authentication successful")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    label,
		Storage:  tokenStorage,
		Metadata: metadata,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  tokenStorage.BaseURL,
		},
	}, nil
}
