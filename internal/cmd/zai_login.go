package cmd

import (
	"context"
	"fmt"
	"strings"

	zaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/zai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// DoZAILogin runs the ZCode OAuth flow for a Z.AI / BigModel coding plan and saves
// the credentials. provider selects the identity provider: "zai" (international) or
// "bigmodel" (China mainland); it defaults to "zai".
func DoZAILogin(cfg *config.Config, options *LoginOptions, provider string) {
	if options == nil {
		options = &LoginOptions{}
	}

	provider = zaiauth.NormalizeProvider(provider)

	manager := newAuthManager()
	authOpts := &sdkAuth.LoginOptions{
		NoBrowser:    options.NoBrowser,
		Metadata:     map[string]string{"provider": provider},
		Prompt:       options.Prompt,
		CallbackPort: options.CallbackPort,
	}

	record, savedPath, err := manager.Login(context.Background(), "zai", cfg, authOpts)
	if err != nil {
		log.Errorf("Z.AI authentication failed: %v", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && strings.TrimSpace(record.Label) != "" {
		fmt.Printf("Authenticated as %s\n", record.Label)
	}
	fmt.Println("Z.AI authentication successful!")
}
