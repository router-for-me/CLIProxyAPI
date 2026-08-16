package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// LoginOptions contains options for the login processes.
// It provides configuration for authentication flows including browser behavior
// and interactive prompting capabilities.
type LoginOptions struct {
	// NoBrowser indicates whether to skip opening the browser automatically.
	NoBrowser bool

	// CallbackPort overrides the local OAuth callback port when set (>0).
	CallbackPort int

	// Prompt allows the caller to provide interactive input when needed.
	Prompt func(prompt string) (string, error)
}

// DoCodexLogin triggers the Codex OAuth flow through the shared authentication manager.
// It initiates the OAuth authentication process for OpenAI Codex services and saves
// the authentication tokens to the configured auth directory.
//
// Parameters:
//   - cfg: The application configuration
//   - options: Login options including browser behavior and prompts
func DoCodexLogin(cfg *config.Config, options *LoginOptions) {
	doCodexLogin(cfg, options, "Codex")
}

// DoOpenAILogin triggers the OpenAI account OAuth flow while retaining the
// canonical Codex credential provider internally.
func DoOpenAILogin(cfg *config.Config, options *LoginOptions) {
	doCodexLogin(cfg, options, "OpenAI account")
}

func doCodexLogin(cfg *config.Config, options *LoginOptions, providerLabel string) {
	if options == nil {
		options = &LoginOptions{}
	}

	promptFn := options.Prompt
	if promptFn == nil {
		promptFn = defaultProjectPrompt()
	}

	manager := newAuthManager()

	authOpts := &sdkAuth.LoginOptions{
		NoBrowser:    options.NoBrowser,
		CallbackPort: options.CallbackPort,
		Metadata:     map[string]string{},
		Prompt:       promptFn,
	}

	_, savedPath, err := manager.Login(context.Background(), "codex", cfg, authOpts)
	if err != nil {
		if authErr, ok := errors.AsType[*codex.AuthenticationError](err); ok {
			log.Error(codex.GetUserFriendlyMessage(authErr))
			if authErr.Type == codex.ErrPortInUse.Type {
				os.Exit(codex.ErrPortInUse.Code)
			}
			return
		}
		fmt.Printf("%s authentication failed: %v\n", providerLabel, err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	fmt.Printf("%s authentication successful!\n", providerLabel)
}
