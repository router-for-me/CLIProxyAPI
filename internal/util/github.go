package util

import (
	"os"
	"strings"
)

// ResolveGitHubToken returns the configured GitHub API token in priority order:
// 1. GITHUB_TOKEN
// 2. github_token
// 3. GITSTORE_GIT_TOKEN (only if GITSTORE_GIT_URL points to github.com)
func ResolveGitHubToken() string {
	for _, name := range []string{"GITHUB_TOKEN", "github_token"} {
		if token := strings.TrimSpace(os.Getenv(name)); token != "" {
			return token
		}
	}

	gitURL := strings.ToLower(strings.TrimSpace(os.Getenv("GITSTORE_GIT_URL")))
	if !strings.Contains(gitURL, "github.com") {
		return ""
	}

	return strings.TrimSpace(os.Getenv("GITSTORE_GIT_TOKEN"))
}
