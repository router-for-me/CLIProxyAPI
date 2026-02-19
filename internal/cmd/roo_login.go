package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	log "github.com/sirupsen/logrus"
)

const rooInstallHint = "Install: curl -fsSL https://raw.githubusercontent.com/RooCodeInc/Roo-Code/main/apps/cli/install.sh | sh"

// NativeCLIRunner runs a native CLI login and returns (exitCode, error).
// Used for dependency injection in tests.
type NativeCLIRunner func(spec NativeCLISpec) (exitCode int, err error)

// RunRooLoginWithRunner runs Roo login with the given runner. Returns exit code to pass to os.Exit.
// Writes success/error messages to stdout/stderr. Used for testability.
func RunRooLoginWithRunner(runner NativeCLIRunner, stdout, stderr io.Writer) int {
	if runner == nil {
		runner = RunNativeCLILogin
	}
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	exitCode, err := runner(RooSpec)
	if err != nil {
		log.Errorf("Roo login failed: %v", err)
		_, _ = fmt.Fprintf(stderr, "\n%s\n", rooInstallHint)
		return 1
	}
	if exitCode != 0 {
		return exitCode
	}
	_, _ = fmt.Fprintln(stdout, "Roo authentication successful!")
	_, _ = fmt.Fprintln(stdout, "Add a roo: block to your config with token-file: \"~/.roo/oauth-token.json\" and base-url: \"https://api.roocode.com/v1\"")
	return 0
}

// DoRooLogin runs the Roo native CLI (roo auth login) for authentication.
// Roo stores tokens in ~/.roo/; add a roo: block to config with token-file pointing to that location.
//
// Parameters:
//   - cfg: The application configuration (used for auth-dir context; roo uses its own paths)
//   - options: Login options (unused for native CLI; kept for API consistency)
func DoRooLogin(cfg *config.Config, options *LoginOptions) {
	_ = cfg
	_ = options
	os.Exit(RunRooLoginWithRunner(RunNativeCLILogin, nil, nil))
}
