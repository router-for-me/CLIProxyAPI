package githubapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTokenFromEnvPrefersGitHubToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp-test")
	t.Setenv("github_token", "ghp-lowercase")
	t.Setenv("GITSTORE_GIT_URL", "https://github.com/example/repo.git")
	t.Setenv("GITSTORE_GIT_TOKEN", "ghp-gitstore")

	if got := TokenFromEnv(); got != "ghp-test" {
		t.Fatalf("TokenFromEnv() = %q, want %q", got, "ghp-test")
	}
}

func TestTokenFromEnvFallsBackToGitStoreToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("github_token", "")
	t.Setenv("GITSTORE_GIT_URL", "https://github.com/example/repo.git")
	t.Setenv("GITSTORE_GIT_TOKEN", "ghp-gitstore")

	if got := TokenFromEnv(); got != "ghp-gitstore" {
		t.Fatalf("TokenFromEnv() = %q, want %q", got, "ghp-gitstore")
	}
}

func TestApplyRequestHeadersAddsAuthorization(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp-test")

	req := httptest.NewRequest(http.MethodGet, "https://api.github.com/repos/example/repo/releases/latest", nil)
	ApplyRequestHeaders(req, "CLIProxyAPI-test")

	if got := req.Header.Get("Authorization"); got != "Bearer ghp-test" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer ghp-test")
	}
	if got := req.Header.Get("Accept"); got != "application/vnd.github+json" {
		t.Fatalf("Accept = %q, want application/vnd.github+json", got)
	}
	if got := req.Header.Get("User-Agent"); got != "CLIProxyAPI-test" {
		t.Fatalf("User-Agent = %q, want CLIProxyAPI-test", got)
	}
}
