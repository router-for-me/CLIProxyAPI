package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	metaauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/meta"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func (a MetaAuthenticator) metaAuth(cfg *config.Config) *metaauth.MetaAuth {
	svc := metaauth.NewMetaAuth(cfg)
	if a.httpClient != nil {
		svc.SetHTTPClient(a.httpClient)
	}
	return svc
}

func (a MetaAuthenticator) effectiveInterval(d time.Duration) time.Duration {
	if a.pollIntervalOverride > 0 {
		return a.pollIntervalOverride
	}
	if d <= 0 {
		return metaauth.DefaultPollInterval
	}
	return d
}

// StartDeviceFlow requests a Meta device code and returns it without waiting
// for the user to authorize.
func (a MetaAuthenticator) StartDeviceFlow(ctx context.Context, cfg *config.Config) (*DeviceFlow, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	deviceCode, err := a.metaAuth(cfg).StartDeviceFlow(ctx)
	if err != nil {
		return nil, fmt.Errorf("meta: failed to start device flow: %w", err)
	}
	if deviceCode == nil || strings.TrimSpace(deviceCode.DeviceCode) == "" {
		return nil, fmt.Errorf("meta: failed to start device flow: empty device code")
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

// PollDeviceFlow exchanges a Meta device code once.
// A pending result means the user has not approved yet.
func (a MetaAuthenticator) PollDeviceFlow(ctx context.Context, cfg *config.Config, flow *DeviceFlow) (*DeviceFlowPoll, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if flow == nil || strings.TrimSpace(flow.DeviceCode) == "" {
		return nil, fmt.Errorf("meta device flow is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	interval := a.effectiveInterval(flow.Interval)
	svc := a.metaAuth(cfg)
	bundle, next, err := svc.PollDeviceCodeOnce(ctx, &metaauth.DeviceCodeResponse{
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
	if bundle == nil {
		return &DeviceFlowPoll{Pending: true, Interval: a.effectiveInterval(next)}, nil
	}
	record, errRecord := a.authFromMetaBundle(svc, bundle)
	if errRecord != nil {
		return nil, errRecord
	}
	return &DeviceFlowPoll{Interval: a.effectiveInterval(next), Auth: record}, nil
}

// CompleteDeviceFlow polls until the Meta device code is authorized or the flow fails.
func (a MetaAuthenticator) CompleteDeviceFlow(ctx context.Context, cfg *config.Config, flow *DeviceFlow) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if flow == nil || strings.TrimSpace(flow.DeviceCode) == "" {
		return nil, fmt.Errorf("meta device flow is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	maxDuration := metaauth.MaxPollDuration
	if flow.ExpiresIn > 0 && flow.ExpiresIn < maxDuration {
		maxDuration = flow.ExpiresIn
	}
	ctx, cancel := context.WithTimeout(ctx, maxDuration)
	defer cancel()

	record, err := pollUntilDeviceAuthorized(ctx, a.effectiveInterval(flow.Interval), time.Time{}, nil, func() (*DeviceFlowPoll, error) {
		polled, errPoll := a.PollDeviceFlow(ctx, cfg, flow)
		if errPoll != nil || polled == nil || polled.Auth != nil {
			return polled, errPoll
		}
		polled.Interval = a.effectiveInterval(polled.Interval)
		flow.Interval = polled.Interval
		return polled, nil
	})
	if err != nil && ctx.Err() != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
		return nil, fmt.Errorf("meta auth: authorization timed out or canceled: %w", ctx.Err())
	}
	return record, err
}

func (a MetaAuthenticator) authFromMetaBundle(authSvc *metaauth.MetaAuth, bundle *metaauth.MetaAuthBundle) (*coreauth.Auth, error) {
	tokenStorage := authSvc.CreateTokenStorage(bundle)
	if tokenStorage == nil || strings.TrimSpace(tokenStorage.AccessToken) == "" {
		return nil, fmt.Errorf("meta token storage missing access token")
	}

	fileName := metaauth.CredentialFileName(tokenStorage.Email, tokenStorage.DCAToken)
	label := strings.TrimSpace(tokenStorage.Email)
	if label == "" {
		label = "Meta"
	}

	metadata := map[string]any{
		"type":         "meta",
		"access_token": tokenStorage.AccessToken,
		"token_type":   tokenStorage.TokenType,
		"expires_in":   tokenStorage.ExpiresIn,
		"expired":      tokenStorage.Expired,
		"last_refresh": tokenStorage.LastRefresh,
		"base_url":     tokenStorage.BaseURL,
		"auth_kind":    "oauth",
	}
	if tokenStorage.DCAExpired != "" {
		metadata["dca_expired"] = tokenStorage.DCAExpired
	}
	if tokenStorage.DCAExpiresAt > 0 {
		metadata["dca_expires_at"] = tokenStorage.DCAExpiresAt
	}
	if tokenStorage.APIKey != "" {
		metadata["api_key"] = tokenStorage.APIKey
	}
	if tokenStorage.DCAToken != "" {
		metadata["dca_token"] = tokenStorage.DCAToken
	}
	if tokenStorage.Email != "" {
		metadata["email"] = tokenStorage.Email
	}
	if tokenStorage.Name != "" {
		metadata["name"] = tokenStorage.Name
	}

	attrs := map[string]string{
		"auth_kind": "oauth",
		"base_url":  tokenStorage.BaseURL,
	}
	if tokenStorage.APIKey != "" {
		attrs["api_key"] = tokenStorage.APIKey
	}
	if tokenStorage.DCAToken != "" {
		attrs["dca_token"] = tokenStorage.DCAToken
	}
	if tokenStorage.Email != "" {
		attrs["email"] = tokenStorage.Email
	}

	fmt.Println("Meta authentication successful")

	return &coreauth.Auth{
		ID:         fileName,
		Provider:   a.Provider(),
		FileName:   fileName,
		Label:      label,
		Storage:    tokenStorage,
		Metadata:   metadata,
		Attributes: attrs,
	}, nil
}
