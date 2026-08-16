package cmd

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
)

// DoCursorLogin runs Cursor's browser OAuth flow and saves the resulting credential.
func DoCursorLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}
	manager := newAuthManager()
	record, savedPath, errLogin := manager.Login(context.Background(), "cursor", cfg, &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
	})
	if errLogin != nil {
		fmt.Printf("Cursor authentication failed: %v\n", errLogin)
		return
	}
	if savedPath != "" {
		fmt.Printf("Cursor credentials saved to %s\n", savedPath)
	} else if record != nil {
		fmt.Printf("Cursor credentials acquired for %s\n", record.Label)
	}
}
