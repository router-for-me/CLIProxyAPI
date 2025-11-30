package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// DoKiroLogin triggers the Kiro authentication flow through the shared authentication manager.
// It initiates the authentication process for Amazon Q/CodeWhisperer services and saves
// the authentication tokens to the configured auth directory.
//
// Parameters:
//   - cfg: The application configuration
//   - options: Login options including browser behavior and prompts
func DoKiroLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	// Setup default prompt function if not provided
	promptFn := options.Prompt
	if promptFn == nil {
		reader := bufio.NewReader(os.Stdin)
		promptFn = func(prompt string) (string, error) {
			fmt.Print(prompt)
			value, err := reader.ReadString('\n')
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(value), nil
		}
	}

	manager := newAuthManager()

	authOpts := &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
		Metadata:  map[string]string{},
		Prompt:    promptFn,
	}

	_, savedPath, err := manager.Login(context.Background(), "kiro", cfg, authOpts)
	if err != nil {
		log.Errorf("Kiro authentication failed: %v", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}

	fmt.Println("Kiro authentication successful!")
}
