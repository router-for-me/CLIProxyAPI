package util

import "github.com/router-for-me/CLIProxyAPI/v8/internal/githubauth"

// ResolveGitHubToken returns the GitHub API token in priority order:
// 1. server.github-token
// 2. GITHUB_TOKEN
// 3. github_token
// 4. GITSTORE_GIT_TOKEN (only if GITSTORE_GIT_URL points to github.com)
func ResolveGitHubToken() string {
	return githubauth.ResolveToken()
}
