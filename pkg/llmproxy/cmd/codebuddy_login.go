package cmd

import (
	"context"
	"fmt"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/config"
	sdkAuth "github.com/kooshapari/CLIProxyAPI/v7/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// DoCodeBuddyLogin triggers the browser OAuth polling flow for CodeBuddy and saves tokens.
// It initiates the OAuth authentication, displays the user code for the user to enter
// at the CodeBuddy verification URL, and waits for authorization before saving the tokens.
//
// Parameters:
//   - cfg: The application configuration containing proxy and auth directory settings
//   - options: Login options including browser behavior settings
func DoCodeBuddyLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	manager := newAuthManager()
	authOpts := &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
		Metadata:  map[string]string{},
	}

	record, savedPath, err := manager.Login(context.Background(), "codebuddy", cfg, authOpts)
	if err != nil {
		log.Errorf("CodeBuddy authentication failed: %v", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Authenticated as %s\n", record.Label)
	}
	fmt.Println("CodeBuddy authentication successful!")
}
