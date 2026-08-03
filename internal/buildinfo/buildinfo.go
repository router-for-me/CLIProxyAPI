// Package buildinfo exposes compile-time metadata shared across the server.
package buildinfo

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
