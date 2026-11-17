// Package selfupdate implements the proxx self-updater (ADR 0007): it checks GitHub releases,
// downloads and verifies a signed release, and restarts the server in place (same PID, same
// listening socket) once no request is in flight. Linux and macOS only; elsewhere it only notifies.
package selfupdate

// Build-time constants. They can be overridden ONLY with -ldflags -X (tests and release pinning);
// they are never read from config, environment or a request.
var (
	// apiBaseURL is the GitHub API base URL.
	apiBaseURL = "https://api.github.com"
	// repo is the GitHub repository that publishes proxx releases.
	repo = "iamkunal9/proxx"
	// releasePath is the release endpoint below /repos/<repo>/ (e.g. "releases/tags/v1.2.3-rc1" to pin).
	releasePath = "releases/latest"
	// allowPrerelease = "true" accepts a prerelease (only useful together with a pinned releasePath).
	allowPrerelease = ""
	// publicKeyB64 is the base64 ed25519 public key that signs checksums.txt.
	publicKeyB64 = "f1FBolCTWjL8K973t2XK+u+TzLp7ZnVybU/v8NgOFxc="
	// testCrashOnStart = "1" makes the binary exit right after the startup bookkeeping (e2e rollback test).
	testCrashOnStart = ""
)

// APILevel is the backend API level reported to the management panel. Bump it when the panel needs
// a backend feature that older backends lack; panel releases declare `proxx-min-backend-api: N`.
const APILevel = 1

func releaseURL() string {
	return apiBaseURL + "/repos/" + repo + "/" + releasePath
}
