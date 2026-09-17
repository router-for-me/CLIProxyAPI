package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// zcodeProviderFlag normalizes the --zcode-provider value ("zai" global,
// default; "bigmodel" China).
func zcodeProviderFlag(provider string) string {
	if strings.EqualFold(strings.TrimSpace(provider), "bigmodel") {
		return "bigmodel"
	}
	return "zai"
}

// DoZCodeLogin triggers the ZCode OAuth login and saves the credential.
//
// Parameters:
//   - cfg: The application configuration containing proxy and auth directory settings
//   - options: Login options including browser behavior settings
//   - provider: OAuth provider variant ("zai" global or "bigmodel" China)
func DoZCodeLogin(cfg *config.Config, options *LoginOptions, provider string) {
	if options == nil {
		options = &LoginOptions{}
	}

	manager := newAuthManager()
	authOpts := &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
		Metadata:  map[string]string{"provider": zcodeProviderFlag(provider)},
		Prompt:    options.Prompt,
	}

	record, savedPath, err := manager.Login(context.Background(), "zcode", cfg, authOpts)
	if err != nil {
		log.Errorf("ZCode authentication failed: %v", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Authenticated as %s\n", record.Label)
	}
	fmt.Println("ZCode authentication successful!")
}
