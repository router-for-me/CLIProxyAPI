package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
	log "github.com/sirupsen/logrus"
)

const kiloInstallHint = "Install: pnpm add -g @kilocode/cli or see https://github.com/Kilo-Org/kilocode"

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
	_, _ = fmt.Fprintln(stdout, "Add a kilo: block to your config with token-file and base-url: \"https://api.kilo.ai/v1\"")
	return 0
}

// DoKiloLogin runs the Kilo native CLI (kilo auth or kilocode auth) for authentication.
// Kilo stores tokens in its own location; add a kilo: block to config with token-file pointing to that location.
//
// Parameters:
//   - cfg: The application configuration (used for auth-dir context; kilo uses its own paths)
//   - options: Login options (unused for native CLI; kept for API consistency)
func DoKiloLogin(cfg *config.Config, options *LoginOptions) {
	_ = cfg
	_ = options
	os.Exit(RunKiloLoginWithRunner(RunNativeCLILogin, nil, nil))
}
