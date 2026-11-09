package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cmd"
)

const hostRuntimeContractVersion = "1"

// printHostVersion handles host probes before config loading or discovery output.
func printHostVersion(args []string, output io.Writer) bool {
	if !versionRequested(args) {
		return false
	}
	_, _ = fmt.Fprintf(output, "CLIProxyAPI Version: %s, Commit: %s, BuiltAt: %s\n", buildinfo.Version, buildinfo.Commit, buildinfo.BuildDate)
	return true
}

func versionRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--version" || arg == "-version" {
			return true
		}
	}
	return false
}

func hostOptionsFromEnv(parentPID int) cmd.HostOptions {
	return cmd.HostOptions{
		RuntimeContractVersion: hostRuntimeContractVersion,
		ParentPID:              parentPID,
		EphemeralAPIKey:        strings.TrimSpace(os.Getenv("CLIPROXY_EPHEMERAL_API_KEY")),
	}
}

// applyHostSafeMode preserves upstream decisions unless the host supplies a key.
func applyHostSafeMode(enabled bool, options cmd.HostOptions) bool {
	return enabled && strings.TrimSpace(options.EphemeralAPIKey) == ""
}
