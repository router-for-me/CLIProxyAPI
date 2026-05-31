package cmd

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// DoGrokLogin drives the SuperGrok OAuth flow (browser loopback by default,
// device-code on --no-browser or when port 56121 is occupied).
//
// Grok-specific UX:
//   - Setting --oauth-callback-port emits a warning and is ignored. xAI
//     registers redirect_uris with the shared Grok-CLI client and rejects
//     any port other than 56121.
//   - When the loopback port is occupied (likely because Grok-CLI itself
//     is bound), the flow auto-falls-back to device-code without requiring
//     the user to add --no-browser.
func DoGrokLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	if options.CallbackPort != 0 && options.CallbackPort != 56121 {
		log.Warnf("--oauth-callback-port=%d is ignored for Grok: xAI rejects any redirect port other than 56121.", options.CallbackPort)
	}

	manager := newAuthManager()
	authOpts := &sdkAuth.LoginOptions{
		NoBrowser:    options.NoBrowser,
		CallbackPort: options.CallbackPort,
		Metadata:     map[string]string{},
		Prompt:       options.Prompt,
	}

	record, savedPath, err := manager.Login(context.Background(), "grok", cfg, authOpts)
	if err != nil {
		log.Errorf("Grok authentication failed: %v", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Authenticated as %s\n", record.Label)
	}
	fmt.Println("Grok authentication successful!")
}
