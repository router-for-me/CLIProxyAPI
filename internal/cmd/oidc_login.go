package cmd

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/oidc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
)

type OIDCLoginParams struct {
	Name string
}

func DoOIDCLogin(cfg *config.Config, params *OIDCLoginParams, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}
	if params == nil {
		params = &OIDCLoginParams{}
	}

	var err error
	promptFn := options.Prompt
	if promptFn == nil {
		promptFn = defaultProjectPrompt()
	}

	manager := newAuthManager()

	authOpts := &sdkAuth.LoginOptions{
		NoBrowser:    options.NoBrowser,
		CallbackPort: options.CallbackPort,
		Prompt:       promptFn,
		Metadata: map[string]string{
			oidc.MetadataNameKey: params.Name,
		},
	}

	_, savedPath, err := manager.Login(context.Background(), "oidc", cfg, authOpts)
	if err != nil {
		fmt.Printf("OIDC authentication failed: %v\n", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	fmt.Println("OIDC authentication successful!")
}
