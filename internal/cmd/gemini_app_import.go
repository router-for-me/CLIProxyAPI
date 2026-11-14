package cmd

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/geminiapp"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// DoGeminiAppImport validates and persists cookies exported from a signed-in browser.
func DoGeminiAppImport(cfg *config.Config, path, account string) {
	_, savedPath, err := newAuthManager().Login(context.Background(), geminiapp.Provider, cfg, &sdkAuth.LoginOptions{Metadata: map[string]string{"cookie-file": path, "account": account}})
	if err != nil {
		log.Errorf("Gemini App authentication failed: %v", err)
		return
	}
	fmt.Printf("Gemini App authentication saved to %s\n", savedPath)
}
