package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// CopilotAuthenticator implements the device flow login for GitHub Copilot accounts.
type CopilotAuthenticator struct{}

// NewCopilotAuthenticator constructs a Copilot authenticator.
func NewCopilotAuthenticator() *CopilotAuthenticator {
	return &CopilotAuthenticator{}
}

func (a *CopilotAuthenticator) Provider() string {
	return "copilot"
}

func (a *CopilotAuthenticator) RefreshLead() *time.Duration {
	// Copilot tokens expire ~25 minutes, refresh every 20 minutes to be safe
	d := 20 * time.Minute
	return &d
}

func (a *CopilotAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	authSvc := copilot.NewCopilotAuth(cfg)

	// Step 1: Initiate GitHub device flow
	fmt.Println("Initiating GitHub Copilot authentication...")
	deviceResp, err := authSvc.InitiateDeviceFlow(ctx)
	if err != nil {
		return nil, fmt.Errorf("GitHub device flow initiation failed: %w", err)
	}

	authURL := deviceResp.VerificationURI
	userCode := deviceResp.UserCode

	fmt.Printf("\nPlease visit: %s\n", authURL)
	fmt.Printf("And enter code: %s\n\n", userCode)

	if !opts.NoBrowser {
		fmt.Println("Opening browser for GitHub authentication...")
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the URL manually")
		} else if err = browser.OpenURL(authURL); err != nil {
			log.Warnf("Failed to open browser automatically: %v", err)
		}
	}

	fmt.Println("Waiting for GitHub authorization...")

	// Step 2: Poll for GitHub access token
	githubToken, err := authSvc.PollForGitHubToken(deviceResp.DeviceCode, deviceResp.Interval)
	if err != nil {
		return nil, fmt.Errorf("GitHub authentication failed: %w", err)
	}

	fmt.Println("GitHub authentication successful! Exchanging for Copilot token...")

	// Step 3: Exchange GitHub token for Copilot token
	copilotTokenData, err := authSvc.RefreshCopilotToken(ctx, githubToken)
	if err != nil {
		return nil, fmt.Errorf("Copilot token exchange failed: %w", err)
	}

	tokenStorage := authSvc.CreateTokenStorage(copilotTokenData)

	// Get email for identification
	email := ""
	if opts.Metadata != nil {
		email = opts.Metadata["email"]
		if email == "" {
			email = opts.Metadata["alias"]
		}
	}

	if email == "" && opts.Prompt != nil {
		email, err = opts.Prompt("Please input your email address or alias for GitHub Copilot:")
		if err != nil {
			return nil, err
		}
	}

	email = strings.TrimSpace(email)
	if email == "" {
		return nil, &EmailRequiredError{Prompt: "Please provide an email address or alias for GitHub Copilot."}
	}

	tokenStorage.Email = email

	fileName := fmt.Sprintf("copilot-%s.json", tokenStorage.Email)
	metadata := map[string]any{
		"email": tokenStorage.Email,
		"sku":   tokenStorage.SKU,
	}

	fmt.Printf("GitHub Copilot authentication successful! (SKU: %s)\n", tokenStorage.SKU)

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}
