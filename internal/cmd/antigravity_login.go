package cmd

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	log "github.com/sirupsen/logrus"
)

// DoAntigravityLogin performs the Antigravity OAuth login.
func DoAntigravityLogin(cfg *config.Config) {
	auth := antigravity.NewAntigravityAuth(cfg)

	tokenData, err := auth.StartOAuthFlow(context.Background())
	if err != nil {
		log.Errorf("Antigravity OAuth flow failed: %v", err)
		return
	}

	tokenStorage := auth.CreateTokenStorage(tokenData)

	authFilePath := getAuthFilePath(cfg, "antigravity", tokenData.Email)

	if err := tokenStorage.SaveTokenToFile(authFilePath); err != nil {
		log.Errorf("Failed to save Antigravity authentication: %v", err)
		return
	}

	log.Infof("Antigravity authentication successful! User: %s", tokenData.Email)
	if tokenData.IsGoogleInternal {
		log.Info("Google internal user detected - special features enabled")
	}
	log.Infof("Authentication saved to: %s", authFilePath)
}
