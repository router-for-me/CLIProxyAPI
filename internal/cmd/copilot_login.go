package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
)

// DoCopilotLogin triggers the GitHub Copilot device code OAuth flow.
// It initiates the OAuth authentication process for GitHub Copilot services and saves
// the authentication tokens to the configured auth directory.
//
// Account type selection: When cfg.CopilotKey is configured, this function uses the
// account_type from the FIRST entry (cfg.CopilotKey[0].AccountType) to determine
// whether to authenticate as individual, business, or enterprise. If no CopilotKey
// entries exist or the first entry has no account_type, it defaults to "individual".
//
// To use a different account type, either:
//   - Reorder the copilot-api-key entries in config.yaml so the desired one is first
//   - Or use the management API /copilot-auth-url endpoint with explicit account_type param
//
// Parameters:
//   - cfg: The application configuration
//   - options: Login options including browser behavior and prompts
func DoCopilotLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	manager := newAuthManager()

	authOpts := &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
		Metadata:  map[string]string{},
		Prompt:    options.Prompt,
	}

	// Use account type from first CopilotKey entry if configured, with validation
	if len(cfg.CopilotKey) > 0 && cfg.CopilotKey[0].AccountType != "" {
		accountTypeStr := cfg.CopilotKey[0].AccountType
		validation := copilot.ValidateAccountType(accountTypeStr)
		if !validation.Valid {
			fmt.Printf("Warning: %s\n", validation.ErrorMessage)
		} else {
			authOpts.Metadata["account_type"] = accountTypeStr
		}
	}

	// Create a context that cancels on SIGINT/SIGTERM for graceful abort
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	_, savedPath, err := manager.Login(ctx, "copilot", cfg, authOpts)
	if err != nil {
		fmt.Printf("Copilot authentication failed: %v\n", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	fmt.Println("Copilot authentication successful!")
}
