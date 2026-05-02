// Package gitlab provides authentication support for GitLab Duo.
package gitlab

import (
	"context"
	"fmt"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/config"
	coreauth "github.com/kooshapari/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// GitLabAuth handles GitLab Duo authentication flow.
type GitLabAuth struct {
	cfg *config.Config
}

// NewGitLabAuth creates a new GitLab auth handler.
func NewGitLabAuth(cfg *config.Config) *GitLabAuth {
	return &GitLabAuth{cfg: cfg}
}

// GenerateAuthURL generates the OAuth authorization URL for GitLab.
func (g *GitLabAuth) GenerateAuthURL(state string) (string, error) {
	// GitLab Duo uses OAuth2
	// Return a placeholder URL - actual implementation would use GitLab's OAuth endpoint
	return "https://gitlab.com/oauth/authorize?client_id=cursor&redirect_uri=callback&response_type=code&state=" + state, nil
}

// ExchangePAT exchanges a Personal Access Token for an auth record.
func (g *GitLabAuth) ExchangePAT(ctx context.Context, token string) (*coreauth.Auth, error) {
	if token == "" {
		return nil, fmt.Errorf("gitlab auth: token is required")
	}

	// Create auth record with the PAT
	record := &coreauth.Auth{
		ID:       fmt.Sprintf("gitlab-%s", token[:8]),
		Provider: "gitlab",
		Attributes: map[string]string{
			"type":  "pat",
			"token": token,
		},
	}

	return record, nil
}
