package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
	log "github.com/sirupsen/logrus"
)

const thegentInstallHint = "Install: pipx install thegent (or pip install -U thegent)"

// RunThegentLoginWithRunner runs TheGent unified login for a provider.
func RunThegentLoginWithRunner(runner NativeCLIRunner, stdout, stderr io.Writer, provider string) int {
	if runner == nil {
		runner = RunNativeCLILogin
	}
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}

	provider = strings.TrimSpace(provider)
	if provider == "" {
		_, _ = fmt.Fprintln(stderr, "provider is required for --thegent-login (example: --thegent-login=codex)")
		return 1
	}

	exitCode, err := runner(ThegentSpec(provider))
	if err != nil {
		log.Errorf("TheGent login failed: %v", err)
		_, _ = fmt.Fprintf(stderr, "\n%s\n", thegentInstallHint)
		return 1
	}
	if exitCode != 0 {
		return exitCode
	}
	_, _ = fmt.Fprintf(stdout, "TheGent authentication successful for provider %q!\n", provider)
	return 0
}

// DoThegentLogin runs TheGent unified provider login flow.
func DoThegentLogin(cfg *config.Config, options *LoginOptions, provider string) {
	_ = cfg
	_ = options
	os.Exit(RunThegentLoginWithRunner(RunNativeCLILogin, nil, nil, provider))
}
