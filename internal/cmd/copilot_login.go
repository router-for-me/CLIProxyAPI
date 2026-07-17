package cmd

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
)

func DoCopilotLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	manager := newAuthManager()
	authOpts := &sdkAuth.LoginOptions{
		NoBrowser:    options.NoBrowser,
		CallbackPort: options.CallbackPort,
		Metadata:     map[string]string{},
		Prompt:       options.Prompt,
	}

	_, savedPath, err := manager.Login(context.Background(), "copilot", cfg, authOpts)
	if err != nil {
		fmt.Printf("GitHub Copilot authentication failed: %v\n", err)
		return
	}
	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	fmt.Println("GitHub Copilot authentication successful!")
}
