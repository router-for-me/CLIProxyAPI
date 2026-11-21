package cmd

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	log "github.com/sirupsen/logrus"
)

func DoCopilotLogin(cfg *config.Config, opts *LoginOptions) {
	if opts == nil {
		opts = &LoginOptions{}
	}
	_, path, err := newAuthManager().Login(context.Background(), copilot.Provider, cfg, &sdkauth.LoginOptions{NoBrowser: opts.NoBrowser, ProxyURL: opts.ProxyURL})
	if err != nil {
		log.Errorf("Copilot authentication failed: %v", err)
		return
	}
	if path != "" {
		fmt.Printf("Authentication saved to %s\n", path)
	}
	fmt.Println("Copilot authentication successful!")
}
