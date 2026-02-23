package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
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
	exitCode := RunKiloLoginWithRunner(RunNativeCLILogin, os.Stdout, os.Stderr)
	if exitCode != 0 {
		os.Exit(exitCode)
	}
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
