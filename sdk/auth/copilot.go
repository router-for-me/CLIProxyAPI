package auth

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type CopilotAuthenticator struct{}

func (CopilotAuthenticator) Provider() string { return copilot.Provider }

func (CopilotAuthenticator) RefreshLead() *time.Duration {
	lead := 5 * time.Minute
	return &lead
}

func (CopilotAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("copilot: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	client := util.SetProxy(&cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second})
	device, err := copilot.StartDeviceFlow(ctx, client)
	if err != nil {
		return nil, err
	}
	fmt.Printf("Open %s and enter Copilot device code: %s\n", device.VerificationURI, device.UserCode)
	if (opts == nil || !opts.NoBrowser) && browser.IsAvailable() {
		if errOpen := browser.OpenURL(device.VerificationURI); errOpen != nil {
			log.Warn("Could not open browser; visit the Copilot device URL manually")
		}
	}
	return CompleteCopilotDeviceLogin(ctx, client, device)
}

// CompleteCopilotDeviceLogin exchanges an authorized GitHub device code for a Copilot credential.
func CompleteCopilotDeviceLogin(ctx context.Context, client *http.Client, device copilot.DeviceCode) (*coreauth.Auth, error) {
	githubToken, err := copilot.PollDeviceFlow(ctx, client, device)
	if err != nil {
		return nil, err
	}
	account, err := copilot.GetAccount(ctx, client, githubToken)
	if err != nil {
		return nil, err
	}
	token, err := copilot.Exchange(ctx, client, githubToken)
	if err != nil {
		return nil, err
	}
	base, err := token.API()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	id := fmt.Sprintf("copilot-%d.json", account.ID)
	return &coreauth.Auth{
		ID: id, FileName: id, Provider: copilot.Provider, Label: account.Login, Status: coreauth.StatusActive,
		CreatedAt: now, UpdatedAt: now, LastRefreshedAt: now,
		Metadata: map[string]any{"type": copilot.Provider, "account_id": fmt.Sprint(account.ID), "username": account.Login, "access_token": token.Value, "refresh_token": githubToken, "base_url": base, "expired": time.Unix(token.ExpiresAt, 0).UTC().Format(time.RFC3339)},
	}, nil
}
