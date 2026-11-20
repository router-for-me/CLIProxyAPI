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
	fmt.Printf("Open %s and enter GitHub Copilot device code: %s\n", device.VerificationURI, device.UserCode)
	if (opts == nil || !opts.NoBrowser) && browser.IsAvailable() {
		if errOpen := browser.OpenURL(device.VerificationURI); errOpen != nil {
			log.Warn("Could not open browser; visit the Copilot device URL manually")
		}
	}
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
	record := &coreauth.Auth{
		ID: id, FileName: id, Provider: copilot.Provider, Label: account.Login, Status: coreauth.StatusActive,
		CreatedAt: now, UpdatedAt: now, LastRefreshedAt: now,
		Metadata: map[string]any{"type": copilot.Provider, "account_id": fmt.Sprint(account.ID), "username": account.Login, "access_token": token.Value, "refresh_token": githubToken, "base_url": base, "expired": time.Unix(token.ExpiresAt, 0).UTC().Format(time.RFC3339)},
	}
	catalogClient := *client
	catalogClient.Timeout = 0
	catalog, errCatalog := copilot.FetchCatalog(ctx, &catalogClient, base, token.Value)
	if errCatalog == nil {
		record.Metadata[copilot.CatalogKey] = catalog
	} else if ctx.Err() != nil {
		return nil, ctx.Err()
	} else {
		log.Warn("Copilot login succeeded, but the account model catalog could not be loaded; it will be fetched at refresh")
	}
	return record, nil
}
