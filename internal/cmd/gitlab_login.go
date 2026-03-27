package cmd

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
)

func DoGitLabLogin(cfg *config.Config, options *LoginOptions) {
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
		Metadata: map[string]string{
			"login_mode": "oauth",
		},
		Prompt: promptFn,
	}

	_, savedPath, err := manager.Login(context.Background(), "gitlab", cfg, authOpts)
	if err != nil {
		fmt.Printf("GitLab Duo authentication failed: %v\n", err)
		return
	}
	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	fmt.Println("GitLab Duo authentication successful!")
}

func DoGitLabTokenLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	promptFn := options.Prompt
	if promptFn == nil {
		promptFn = defaultProjectPrompt()
	}

	manager := newAuthManager()
	authOpts := &sdkAuth.LoginOptions{
		Metadata: map[string]string{
			"login_mode": "pat",
		},
		Prompt: promptFn,
	}

	_, savedPath, err := manager.Login(context.Background(), "gitlab", cfg, authOpts)
	if err != nil {
		fmt.Printf("GitLab Duo PAT authentication failed: %v\n", err)
		return
	}
	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	fmt.Println("GitLab Duo PAT authentication successful!")
}
