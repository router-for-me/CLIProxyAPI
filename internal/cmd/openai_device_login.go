package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	log "github.com/sirupsen/logrus"
)

const (
	codexLoginModeMetadataKey = "codex_login_mode"
	codexLoginModeDevice      = "device"
)

// DoCodexDeviceLogin triggers the Codex device-code flow while keeping the
// existing codex-login OAuth callback flow intact.
func DoCodexDeviceLogin(cfg *config.Config, options *LoginOptions) {
	doCodexDeviceLogin(cfg, options, "Codex")
}

// DoOpenAIDeviceLogin triggers the OpenAI account device-code flow while
// retaining the canonical Codex credential provider internally.
func DoOpenAIDeviceLogin(cfg *config.Config, options *LoginOptions) {
	doCodexDeviceLogin(cfg, options, "OpenAI account")
}

func doCodexDeviceLogin(cfg *config.Config, options *LoginOptions, providerLabel string) {
	if options == nil {
		options = &LoginOptions{}
	}

	promptFn := options.Prompt
	if promptFn == nil {
		promptFn = defaultProjectPrompt()
	}

	manager := newAuthManager()

	authOpts := &sdkAuth.LoginOptions{
		NoBrowser:    options.NoBrowser,
		CallbackPort: options.CallbackPort,
		Metadata: map[string]string{
			codexLoginModeMetadataKey: codexLoginModeDevice,
		},
		Prompt: promptFn,
	}

	_, savedPath, err := manager.Login(context.Background(), "codex", cfg, authOpts)
	if err != nil {
		if authErr, ok := errors.AsType[*codex.AuthenticationError](err); ok {
			log.Error(codex.GetUserFriendlyMessage(authErr))
			if authErr.Type == codex.ErrPortInUse.Type {
				os.Exit(codex.ErrPortInUse.Code)
			}
			return
		}
		fmt.Printf("%s device authentication failed: %v\n", providerLabel, err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	fmt.Printf("%s device authentication successful!\n", providerLabel)
}
