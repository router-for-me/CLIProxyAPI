package githubapi

import (
	"net/http"
	"os"
	"strings"
)

// TokenFromEnv returns a GitHub API token from supported environment variables.
// GITHUB_TOKEN is preferred for public API calls such as release checks.
func TokenFromEnv() string {
	for _, key := range []string{"GITHUB_TOKEN", "github_token"} {
		if token := strings.TrimSpace(os.Getenv(key)); token != "" {
			return token
		}
	}

	gitURL := strings.ToLower(strings.TrimSpace(os.Getenv("GITSTORE_GIT_URL")))
	if strings.Contains(gitURL, "github.com") {
		if token := strings.TrimSpace(os.Getenv("GITSTORE_GIT_TOKEN")); token != "" {
			return token
		}
	}

	return ""
}

// ApplyRequestHeaders sets standard GitHub REST API headers on req.
func ApplyRequestHeaders(req *http.Request, userAgent string) {
	if req == nil {
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if trimmed := strings.TrimSpace(userAgent); trimmed != "" {
		req.Header.Set("User-Agent", trimmed)
	}
	if token := TokenFromEnv(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// RequestHeaders returns headers for GitHub REST API requests via httpfetch.
func RequestHeaders(userAgent string) map[string]string {
	headers := map[string]string{
		"Accept":     "application/vnd.github+json",
		"User-Agent": strings.TrimSpace(userAgent),
	}
	if token := TokenFromEnv(); token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	return headers
}
