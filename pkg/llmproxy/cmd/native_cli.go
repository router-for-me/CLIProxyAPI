// Package cmd provides command-line interface functionality for the CLI Proxy API server.
package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// NativeCLISpec defines a provider that uses its own CLI for authentication.
type NativeCLISpec struct {
	// Name is the CLI binary name (e.g. "roo", "kilo").
	Name string
	// Args are the subcommand args (e.g. ["auth", "login"]).
	Args []string
	// FallbackNames are alternative binary names to try (e.g. "kilocode" for kilo).
	FallbackNames []string
}

var (
	// RooSpec defines Roo Code native CLI: roo auth login.
	RooSpec = NativeCLISpec{
		Name:          "roo",
		Args:          []string{"auth", "login"},
		FallbackNames: nil,
	}
	// KiloSpec defines Kilo native CLI: kilo auth or kilocode auth.
	KiloSpec = NativeCLISpec{
		Name:          "kilo",
		Args:          []string{"auth"},
		FallbackNames: []string{"kilocode"},
	}
)

// ThegentSpec returns TheGent CLI login spec for a provider.
// Command shape: thegent cliproxy login <provider>
func ThegentSpec(provider string) NativeCLISpec {
	return NativeCLISpec{
		Name:          "thegent",
		Args:          []string{"cliproxy", "login", provider},
		FallbackNames: nil,
	}
}

// ResolveNativeCLI returns the absolute path to the native CLI binary, or empty string if not found.
// Checks PATH and ~/.local/bin.
func ResolveNativeCLI(spec NativeCLISpec) string {
	names := append([]string{spec.Name}, spec.FallbackNames...)
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil && path != "" {
			return path
		}
		home, err := os.UserHomeDir()
		if err != nil {
			continue
		}
		local := filepath.Join(home, ".local", "bin", name)
		if info, err := os.Stat(local); err == nil && !info.IsDir() {
			return local
		}
	}
	return ""
}

// RunNativeCLILogin executes the native CLI with the given spec.
// Returns the exit code and any error. Exit code is -1 if the binary was not found.
func RunNativeCLILogin(spec NativeCLISpec) (exitCode int, err error) {
	binary := ResolveNativeCLI(spec)
	if binary == "" {
		return -1, fmt.Errorf("%s CLI not found", spec.Name)
	}
	cmd := exec.Command(binary, spec.Args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if runErr := cmd.Run(); runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return -1, runErr
	}
	return 0, nil
}
