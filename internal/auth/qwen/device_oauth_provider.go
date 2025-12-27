package qwen

import (
	"context"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/oauthflow"
)

// DeviceOAuthProvider adapts Qwen device OAuth to the shared oauthflow.ProviderDeviceOAuth interface.
type DeviceOAuthProvider struct {
	auth *QwenAuth
}

func NewDeviceOAuthProvider(auth *QwenAuth) *DeviceOAuthProvider {
	return &DeviceOAuthProvider{auth: auth}
}

func (p *DeviceOAuthProvider) Provider() string {
	return "qwen"
}

func (p *DeviceOAuthProvider) DeviceAuthorize(ctx context.Context) (*oauthflow.DeviceCodeResult, error) {
	if p == nil || p.auth == nil {
		return nil, fmt.Errorf("qwen device oauth provider: auth is nil")
	}
	flow, err := p.auth.InitiateDeviceFlow(ctx)
	if err != nil {
		return nil, err
	}
	if flow == nil {
		return nil, fmt.Errorf("qwen device oauth provider: device flow is nil")
	}
	return &oauthflow.DeviceCodeResult{
		DeviceCode:              strings.TrimSpace(flow.DeviceCode),
		UserCode:                strings.TrimSpace(flow.UserCode),
		VerificationURI:         strings.TrimSpace(flow.VerificationURI),
		VerificationURIComplete: strings.TrimSpace(flow.VerificationURIComplete),
		ExpiresIn:               flow.ExpiresIn,
		Interval:                flow.Interval,
		CodeVerifier:            strings.TrimSpace(flow.CodeVerifier),
	}, nil
}

func (p *DeviceOAuthProvider) DevicePoll(ctx context.Context, device *oauthflow.DeviceCodeResult) (*oauthflow.TokenResult, error) {
	if p == nil || p.auth == nil {
		return nil, fmt.Errorf("qwen device oauth provider: auth is nil")
	}
	if device == nil {
		return nil, fmt.Errorf("qwen device oauth provider: device code is nil")
	}

	flow := &DeviceFlow{
		DeviceCode:              strings.TrimSpace(device.DeviceCode),
		UserCode:                strings.TrimSpace(device.UserCode),
		VerificationURI:         strings.TrimSpace(device.VerificationURI),
		VerificationURIComplete: strings.TrimSpace(device.VerificationURIComplete),
		ExpiresIn:               device.ExpiresIn,
		Interval:                device.Interval,
		CodeVerifier:            strings.TrimSpace(device.CodeVerifier),
	}

	tokenData, err := p.auth.PollForToken(ctx, flow)
	if err != nil {
		return nil, err
	}
	if tokenData == nil {
		return nil, fmt.Errorf("qwen device oauth provider: token result is nil")
	}

	meta := map[string]any{}
	if strings.TrimSpace(tokenData.ResourceURL) != "" {
		meta["resource_url"] = strings.TrimSpace(tokenData.ResourceURL)
	}

	tokenType := strings.TrimSpace(tokenData.TokenType)
	if tokenType == "" {
		tokenType = "Bearer"
	}

	return &oauthflow.TokenResult{
		AccessToken:  strings.TrimSpace(tokenData.AccessToken),
		RefreshToken: strings.TrimSpace(tokenData.RefreshToken),
		ExpiresAt:    strings.TrimSpace(tokenData.Expire),
		TokenType:    tokenType,
		Metadata:     meta,
	}, nil
}

func (p *DeviceOAuthProvider) Refresh(ctx context.Context, refreshToken string) (*oauthflow.TokenResult, error) {
	if p == nil || p.auth == nil {
		return nil, fmt.Errorf("qwen device oauth provider: auth is nil")
	}
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, fmt.Errorf("qwen device oauth provider: refresh token is empty")
	}
	tokenData, err := p.auth.RefreshTokens(ctx, refreshToken)
	if err != nil {
		return nil, err
	}
	if tokenData == nil {
		return nil, fmt.Errorf("qwen device oauth provider: refresh result is nil")
	}

	meta := map[string]any{}
	if strings.TrimSpace(tokenData.ResourceURL) != "" {
		meta["resource_url"] = strings.TrimSpace(tokenData.ResourceURL)
	}
	tokenType := strings.TrimSpace(tokenData.TokenType)
	if tokenType == "" {
		tokenType = "Bearer"
	}

	return &oauthflow.TokenResult{
		AccessToken:  strings.TrimSpace(tokenData.AccessToken),
		RefreshToken: refreshToken,
		ExpiresAt:    strings.TrimSpace(tokenData.Expire),
		TokenType:    tokenType,
		Metadata:     meta,
	}, nil
}

func (p *DeviceOAuthProvider) Revoke(ctx context.Context, token string) error {
	return oauthflow.ErrRevokeNotSupported
}
