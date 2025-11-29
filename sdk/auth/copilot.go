package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Copilot account_type Precedence
//
// The account_type determines which GitHub Copilot API endpoints are used
// (individual vs business/enterprise). The precedence for account_type is:
//
//  1. Auth.Attributes["account_type"] - CANONICAL RUNTIME SOURCE
//     Executors must always read from Attributes, never from storage or config directly.
//
//  2. CopilotTokenStorage.AccountType - Initial seed value
//     Used only to populate Attributes when creating a new auth entry.
//     Storage should NOT overwrite a non-empty Attributes value on reload.
//
//  3. Config (copilot-api-key[].account-type) - Default for new logins
//     Used only during initial OAuth login to seed the storage value.
//
// This precedence ensures stable base-URL selection across reloads and prevents
// oscillation between account types. See internal/watcher/watcher.go SnapshotCoreAuths
// for the reload logic that enforces this precedence.

// CopilotAuthenticator implements the GitHub device code OAuth login flow for Copilot.
type CopilotAuthenticator struct {
	AccountType copilot.AccountType
}

// NewCopilotAuthenticator constructs a Copilot authenticator with default settings.
func NewCopilotAuthenticator() *CopilotAuthenticator {
	return &CopilotAuthenticator{
		AccountType: copilot.AccountTypeIndividual,
	}
}

// NewCopilotAuthenticatorWithAccountType constructs a Copilot authenticator with specified account type.
func NewCopilotAuthenticatorWithAccountType(accountType string) *CopilotAuthenticator {
	parsed, _ := copilot.ParseAccountType(accountType)
	return &CopilotAuthenticator{
		AccountType: parsed,
	}
}

func (a *CopilotAuthenticator) Provider() string {
	return "copilot"
}

func (a *CopilotAuthenticator) RefreshLead() *time.Duration {
	// Copilot tokens typically expire in ~30 minutes, refresh 5 minutes before
	d := 5 * time.Minute
	return &d
}

func (a *CopilotAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("copilot auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	// Check for account type override in metadata
	accountType := a.AccountType
	if opts.Metadata != nil {
		if at, ok := opts.Metadata["account_type"]; ok && at != "" {
			accountType, _ = copilot.ParseAccountType(at)
	}
}

	authSvc := copilot.NewCopilotAuth(cfg)

	// Use the shared helper that performs the complete auth flow and returns
	// both the token storage and suggested filename.
	result, err := authSvc.PerformFullAuthWithFilename(ctx, accountType, func(dc *copilot.DeviceCodeResponse) {
		fmt.Printf("\n=== GitHub Copilot Authentication ===\n")
		fmt.Printf("Please enter the code: \n\n    %s\n\n", dc.UserCode)
		fmt.Printf("At: %s\n\n", dc.VerificationURI)

		if !opts.NoBrowser {
			if !browser.IsAvailable() {
				log.Warn("No browser available; please open the URL manually")
				util.PrintSSHTunnelInstructions(0)
			} else if err := browser.OpenURL(dc.VerificationURI); err != nil {
				log.Warnf("Failed to open browser automatically: %v", err)
				util.PrintSSHTunnelInstructions(0)
			} else {
				fmt.Println("Browser opened. Please complete authentication...")
			}
		}
		fmt.Println("Waiting for authentication...")
	})

	if err != nil {
		return nil, fmt.Errorf("copilot authentication failed: %w", err)
	}

	if result == nil || result.Storage == nil {
		return nil, fmt.Errorf("copilot authentication failed: no token storage returned")
	}

	tokenStorage := result.Storage
	fileName := result.SuggestedFilename

	metadata := map[string]any{
		"email":                tokenStorage.Email,
		"username":             tokenStorage.Username,
		"account_type":         tokenStorage.AccountType,
		"copilot_token_expiry": tokenStorage.CopilotTokenExpiry,
		"github_token":         tokenStorage.GitHubToken,
		"copilot_token":        tokenStorage.CopilotToken,
		"type":                 "copilot",
	}

	fmt.Printf("\nCopilot authentication successful!\n")
	if tokenStorage.Username != "" {
		fmt.Printf("Logged in as: %s\n", tokenStorage.Username)
	}

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Storage:  tokenStorage,
		Metadata: metadata,
		// Attributes["account_type"] is the single canonical source of truth for account type at runtime.
		// CopilotTokenStorage.AccountType and config are used only to seed this initial value.
		// On subsequent reloads, storage and config must NOT overwrite a non-empty Attributes["account_type"].
		// Executor logic should always read from Attributes, not Storage or config directly.
		Attributes: map[string]string{
			"account_type": tokenStorage.AccountType,
		},
	}, nil
}

// RefreshToken refreshes the Copilot token for an existing auth entry.
func (a *CopilotAuthenticator) RefreshToken(ctx context.Context, cfg *config.Config, auth *coreauth.Auth) error {
	if auth == nil || auth.Metadata == nil {
		return fmt.Errorf("copilot refresh: invalid auth entry")
	}

	githubToken, ok := auth.Metadata["github_token"].(string)
	if !ok || githubToken == "" {
		return fmt.Errorf("copilot refresh: missing github token")
	}

	authSvc := copilot.NewCopilotAuth(cfg)

	tokenResp, err := authSvc.GetCopilotToken(ctx, githubToken)
	if err != nil {
		return fmt.Errorf("copilot refresh failed: %w", err)
	}

	copilot.ApplyTokenRefresh(auth, tokenResp, time.Now())
	log.Debug("Copilot token refreshed successfully")

	return nil
}
