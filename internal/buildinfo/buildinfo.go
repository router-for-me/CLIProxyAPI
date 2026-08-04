// Package buildinfo exposes compile-time metadata shared across the server.
package buildinfo

import (
	"os/exec"
	"strings"
)

// The following variables are overridden via ldflags during release builds.
// Defaults cover local development builds.
var (
	// Version is the NixLLM release version (e.g. 0.0.1) of the binary.
	Version = "dev"

	// CoreVersion is the upstream CLIProxyAPI version this binary is rebased
	// on, derived at build time from the latest v7.* git tag.
	CoreVersion = "unknown"

	// Commit is the git commit SHA baked into the binary.
	Commit = "none"

	// BuildDate records when the binary was built in UTC.
	BuildDate = "unknown"
)

// ResolveFromGit fills Version and CoreVersion from the nearest matching git
// tag when ldflags did not bake in release metadata (i.e. plain `go build`
// during development). It is a no-op when the values are already set by a
// release build or no .git tree is reachable. Call once at startup, after the
// ldflag-injected values have been copied in.
func ResolveFromGit() {
	if Version == "dev" {
		if v := describeTag("v0.*"); v != "" {
			Version = v
		}
	}
	if CoreVersion == "unknown" {
		if v := describeTag("v7.*"); v != "" {
			CoreVersion = v
		}
	}
}

// describeTag returns the nearest tag matching pattern without the leading
// "v", or "" when git is unavailable or no tag matches.
func describeTag(pattern string) string {
	out, err := exec.Command("git", "describe", "--tags", "--match", pattern, "--abbrev=0").Output()
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
}
