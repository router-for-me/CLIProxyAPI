/**
 * @file Cline login command implementation
 * @description Handles the Cline authentication flow using refresh tokens. Unlike traditional
 * OAuth flows, Cline uses a simpler approach where users export a refresh token from the
 * VSCode extension and provide it to CLIProxyAPI. This file implements the command-line
 * interface for the Cline login process.
 */

package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// DoClineLogin handles the Cline authentication flow using the shared authentication manager.
// It prompts the user for a refresh token (exported from VSCode), exchanges it for access tokens,
// and saves the authentication credentials to the configured auth directory.
//
// Parameters:
//   - cfg: The application configuration
//   - options: Login options including browser behavior and prompts
func DoClineLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	manager := newAuthManager()

	promptFn := options.Prompt
	if promptFn == nil {
		promptFn = func(prompt string) (string, error) {
			fmt.Println()
			fmt.Println(prompt)
			var value string
			_, err := fmt.Scanln(&value)
			return value, err
		}
	}

	authOpts := &sdkAuth.LoginOptions{
		NoBrowser: true, // Cline doesn't use browser-based OAuth
		Metadata:  map[string]string{},
		Prompt:    promptFn,
	}

	_, savedPath, err := manager.Login(context.Background(), "cline", cfg, authOpts)
	if err != nil {
		var emailErr *sdkAuth.EmailRequiredError
		if errors.As(err, &emailErr) {
			log.Error(emailErr.Error())
			return
		}
		fmt.Printf("Cline authentication failed: %v\n", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}

	fmt.Println("Cline authentication successful!")
}
