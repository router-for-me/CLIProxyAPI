package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	log "github.com/sirupsen/logrus"
)

const kiloInstallHint = "Install: https://www.kiloai.com/download"

// DoKiloLogin handles the Kilo device flow using the shared authentication manager.
// It initiates the device-based authentication process for Kilo AI services and saves
// the authentication tokens to the configured auth directory.
//
// Parameters:
//   - cfg: The application configuration
//   - options: Login options including browser behavior and prompts
func DoKiloLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	manager := newAuthManager()

	promptFn := options.Prompt
	if promptFn == nil {
		promptFn = func(prompt string) (string, error) {
			fmt.Print(prompt)
			var value string
			_, _ = fmt.Scanln(&value)
			return strings.TrimSpace(value), nil
		}
	}

	authOpts := &sdkAuth.LoginOptions{
		NoBrowser:    options.NoBrowser,
		CallbackPort: options.CallbackPort,
		Metadata:     map[string]string{},
		Prompt:       promptFn,
	}

	_, savedPath, err := manager.Login(context.Background(), "kilo", cfg, authOpts)
	if err != nil {
		fmt.Printf("Kilo authentication failed: %v\n", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}

	fmt.Println("Kilo authentication successful!")
}

// RunKiloLoginWithRunner runs Kilo login with the given runner. Returns exit code to pass to os.Exit.
// Writes success/error messages to stdout/stderr. Used for testability.
func RunKiloLoginWithRunner(runner NativeCLIRunner, stdout, stderr io.Writer) int {
	if runner == nil {
		runner = RunNativeCLILogin
	}
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	exitCode, err := runner(KiloSpec)
	if err != nil {
		log.Errorf("Kilo login failed: %v", err)
		_, _ = fmt.Fprintf(stderr, "\n%s\n", kiloInstallHint)
		return 1
	}
	if exitCode != 0 {
		return exitCode
	}
	_, _ = fmt.Fprintln(stdout, "Kilo authentication successful!")
	_, _ = fmt.Fprintln(stdout, "Add a kilo: block to your config with token-file: \"~/.kilo/oauth-token.json\" and base-url: \"https://api.kiloai.com/v1\"")
	return 0
}
