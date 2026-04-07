package cmd

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// DoCursorLogin triggers the OAuth PKCE flow for Cursor and saves tokens.
func DoCursorLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	manager := newAuthManager()
	authOpts := &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
		Metadata:  map[string]string{},
		Prompt:    options.Prompt,
	}

	record, savedPath, err := manager.Login(context.Background(), "cursor", cfg, authOpts)
	if err != nil {
		log.Errorf("Cursor authentication failed: %v", err)
		return
	}

	if savedPath != "" {
		log.Infof("Authentication saved to %s", savedPath)
	}
	if record != nil && record.Label != "" {
		log.Infof("Authenticated as %s", record.Label)
	}
	log.Info("Cursor authentication successful!")
}
