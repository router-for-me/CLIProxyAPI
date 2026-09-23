package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// XAIAuthenticator implements the xAI Grok OAuth device-code flow.
type XAIAuthenticator struct {
	httpClient *http.Client
	// pollIntervalOverride, when set, replaces the wait between device-flow polls.
	// Production leaves it zero so the provider interval is used.
	pollIntervalOverride time.Duration
}

// NewXAIAuthenticator constructs a new xAI authenticator.
func NewXAIAuthenticator() Authenticator {
	return &XAIAuthenticator{}
}

// Provider returns the provider key for xAI.
func (XAIAuthenticator) Provider() string {
	return "xai"
}

// RefreshLead instructs the manager to refresh before token expiry.
func (XAIAuthenticator) RefreshLead() *time.Duration {
	lead := xaiauth.RefreshLead()
	return &lead
}

// Login launches the OAuth device-code flow to obtain xAI tokens and persists them.
func (a XAIAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	fmt.Println("Starting xAI authentication...")
	flow, err := a.StartDeviceFlow(ctx, cfg)
	if err != nil {
		return nil, err
	}

	verificationURL := strings.TrimSpace(flow.VerificationURIComplete)
	if verificationURL == "" {
		verificationURL = strings.TrimSpace(flow.VerificationURI)
	}

	fmt.Printf("\nTo authenticate, please visit:\n%s\n\n", verificationURL)
	if flow.UserCode != "" {
		fmt.Printf("Then enter this code: %s\n\n", flow.UserCode)
	}

	if !opts.NoBrowser {
		if browser.IsAvailable() {
			if errOpen := browser.OpenURL(verificationURL); errOpen != nil {
				log.Warnf("Failed to open browser automatically: %v", errOpen)
			} else {
				fmt.Println("Browser opened automatically.")
			}
		} else {
			log.Warn("No browser available; please open the URL manually")
		}
	}

	fmt.Println("Waiting for authorization...")
	if flow.ExpiresIn > 0 {
		fmt.Printf("(This will timeout in %d seconds if not authorized)\n", int(flow.ExpiresIn/time.Second))
	}

	record, errWait := a.CompleteDeviceFlow(ctx, cfg, flow)
	if errWait != nil {
		return nil, fmt.Errorf("xai: %w", errWait)
	}
	return record, nil
}
