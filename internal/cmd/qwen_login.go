package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// DoQwenLogin handles the Qwen device flow using the shared authentication manager.
// It initiates the device-based authentication process for Qwen services and saves
// the authentication tokens to the configured auth directory.
//
// Parameters:
//   - cfg: The application configuration
//   - options: Login options including browser behavior and prompts
func DoQwenLogin(cfg *config.Config, options *LoginOptions) {
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
		NoBrowser:    options.NoBrowser,
		CallbackPort: options.CallbackPort,
		Metadata:     map[string]string{},
		Prompt:       promptFn,
	}

	_, savedPath, err := manager.Login(context.Background(), "qwen", cfg, authOpts)
	if err != nil {
		if emailErr, ok := errors.AsType[*sdkAuth.EmailRequiredError](err); ok {
			log.Error(emailErr.Error())
			return
		}
		fmt.Printf("Qwen authentication failed: %v\n", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}

	fmt.Println("Qwen authentication successful!")
}
