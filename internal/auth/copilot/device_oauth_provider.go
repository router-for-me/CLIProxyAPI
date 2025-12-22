package copilot

import (
	"context"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/oauthflow"
)

// DeviceOAuthProvider adapts GitHub Copilot device OAuth to the shared oauthflow.ProviderDeviceOAuth interface.
type DeviceOAuthProvider struct {
	client *DeviceFlowClient
}

func NewDeviceOAuthProvider(cfg *config.Config) *DeviceOAuthProvider {
	return &DeviceOAuthProvider{client: NewDeviceFlowClient(cfg)}
}

func (p *DeviceOAuthProvider) Provider() string {
	return "github-copilot"
}

func (p *DeviceOAuthProvider) DeviceAuthorize(ctx context.Context) (*oauthflow.DeviceCodeResult, error) {
	if p == nil || p.client == nil {
		return nil, fmt.Errorf("github-copilot device oauth provider: client is nil")
	}
	device, err := p.client.RequestDeviceCode(ctx)
	if err != nil {
		return nil, err
	}
	if device == nil {
		return nil, fmt.Errorf("github-copilot device oauth provider: device code response is nil")
	}

	return &oauthflow.DeviceCodeResult{
		DeviceCode:      strings.TrimSpace(device.DeviceCode),
		UserCode:        strings.TrimSpace(device.UserCode),
		VerificationURI: strings.TrimSpace(device.VerificationURI),
		ExpiresIn:       device.ExpiresIn,
		Interval:        device.Interval,
	}, nil
}

func (p *DeviceOAuthProvider) DevicePoll(ctx context.Context, device *oauthflow.DeviceCodeResult) (*oauthflow.TokenResult, error) {
	if p == nil || p.client == nil {
		return nil, fmt.Errorf("github-copilot device oauth provider: client is nil")
	}
	if device == nil {
		return nil, fmt.Errorf("github-copilot device oauth provider: device code is nil")
	}

	data, err := p.client.PollForToken(ctx, &DeviceCodeResponse{
		DeviceCode:      strings.TrimSpace(device.DeviceCode),
		UserCode:        strings.TrimSpace(device.UserCode),
		VerificationURI: strings.TrimSpace(device.VerificationURI),
		ExpiresIn:       device.ExpiresIn,
		Interval:        device.Interval,
	})
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, fmt.Errorf("github-copilot device oauth provider: token result is nil")
	}

	meta := map[string]any{}
	if strings.TrimSpace(data.Scope) != "" {
		meta["scope"] = strings.TrimSpace(data.Scope)
	}

	tokenType := strings.TrimSpace(data.TokenType)
	if tokenType == "" {
		tokenType = "bearer"
	}

	return &oauthflow.TokenResult{
		AccessToken: strings.TrimSpace(data.AccessToken),
		TokenType:   tokenType,
		Metadata:    meta,
	}, nil
}

func (p *DeviceOAuthProvider) Refresh(ctx context.Context, refreshToken string) (*oauthflow.TokenResult, error) {
	return nil, oauthflow.ErrRefreshNotSupported
}

func (p *DeviceOAuthProvider) Revoke(ctx context.Context, token string) error {
	return oauthflow.ErrRevokeNotSupported
}
