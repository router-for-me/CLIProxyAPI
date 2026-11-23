package api

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/githubauth"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

func TestServerGitHubTokenReload(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "environment-token")
	t.Cleanup(func() { githubauth.SetToken("") })
	cfg := &config.Config{GitHubToken: "initial-token"}
	server := newTestServerWithConfig(t, cfg)
	if util.ResolveGitHubToken() != "initial-token" {
		t.Fatal("startup did not configure global token")
	}
	updated := cfg.CloneForRuntime()
	updated.GitHubToken = "replacement-token"
	server.UpdateClients(updated)
	if util.ResolveGitHubToken() != "replacement-token" {
		t.Fatal("reload did not replace global token")
	}
	cleared := updated.CloneForRuntime()
	cleared.GitHubToken = ""
	server.UpdateClients(cleared)
	if util.ResolveGitHubToken() != "environment-token" {
		t.Fatal("reload did not clear global token")
	}
}
