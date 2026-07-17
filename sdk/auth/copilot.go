package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type CopilotAuthenticator struct{}

func NewCopilotAuthenticator() Authenticator {
	return &CopilotAuthenticator{}
}

func (CopilotAuthenticator) Provider() string {
	return "copilot"
}

func (CopilotAuthenticator) RefreshLead() *time.Duration {
	lead := copilot.RefreshLead()
	return &lead
}

func (a CopilotAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	authSvc := copilot.NewAuth(cfg, "")
	deviceCode, err := authSvc.StartDeviceFlow(ctx)
	if err != nil {
		return nil, err
	}

	verificationURL := strings.TrimSpace(deviceCode.VerificationURIComplete)
	if verificationURL == "" {
		verificationURL = strings.TrimSpace(deviceCode.VerificationURI)
	}
	if verificationURL == "" {
		verificationURL = copilot.DefaultVerificationURL
	}

	fmt.Println("Starting GitHub Copilot authentication...")
	fmt.Printf("Open this URL to continue:\n%s\n", verificationURL)
	if code := strings.TrimSpace(deviceCode.UserCode); code != "" {
		fmt.Printf("Verification code: %s\n", code)
	}

	if !opts.NoBrowser {
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the URL manually")
		} else if errOpen := browser.OpenURL(verificationURL); errOpen != nil {
			log.Warnf("Failed to open browser automatically: %v", errOpen)
		}
	}

	fmt.Println("Waiting for GitHub device authorization...")
	githubAccessToken, err := authSvc.WaitForAuthorization(ctx, deviceCode)
	if err != nil {
		return nil, err
	}

	sessionToken, err := authSvc.FetchSessionToken(ctx, githubAccessToken)
	if err != nil {
		return nil, err
	}
	availableModels, errModels := authSvc.FetchAvailableModels(ctx, sessionToken.Token, sessionToken.Endpoint)
	if errModels != nil {
		log.Debugf("copilot auth: fetch available models failed: %v", errModels)
	}

	fileName := fmt.Sprintf("copilot-%d.json", time.Now().UnixMilli())
	metadata := map[string]any{
		"type":                "copilot",
		"auth_kind":           "oauth",
		"github_access_token": githubAccessToken,
		"access_token":        sessionToken.Token,
		"expires_at":          sessionToken.ExpiresAt.UTC().Format(time.RFC3339),
		"base_url":            sessionToken.Endpoint,
		"headers":             copilot.DefaultRequestHeaders(),
	}
	if len(availableModels) > 0 {
		metadata["available_models"] = availableModels
	}

	fmt.Println("GitHub Copilot authentication successful")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    "GitHub Copilot",
		Metadata: metadata,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"api_key":   sessionToken.Token,
			"base_url":  sessionToken.Endpoint,
		},
	}, nil
}
