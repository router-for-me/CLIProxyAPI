package cmd

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	log "github.com/sirupsen/logrus"
)

func DoCopilotLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}
	record, path, err := newAuthManager().Login(context.Background(), copilot.Provider, cfg, &sdkAuth.LoginOptions{NoBrowser: options.NoBrowser})
	if err != nil {
		log.WithError(err).Error("GitHub Copilot authentication failed")
		return
	}
	fmt.Printf("Authenticated as %s. Credentials saved to %s\n", record.Label, path)
}
