package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/gemini"
	// legacy client removed
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// GeminiAuthenticator implements the login flow for Google Gemini CLI accounts.
type GeminiAuthenticator struct{}

// NewGeminiAuthenticator constructs a Gemini authenticator.
func NewGeminiAuthenticator() *GeminiAuthenticator {
	return &GeminiAuthenticator{}
}

func (a *GeminiAuthenticator) Provider() string {
	return "gemini"
}

func (a *GeminiAuthenticator) RefreshLead() *time.Duration {
	return nil
}

func (a *GeminiAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	var ts gemini.GeminiTokenStorage
	if opts.ProjectID != "" {
		ts.ProjectID = opts.ProjectID
	}

	geminiAuth := gemini.NewGeminiAuth()
	_, err := geminiAuth.GetAuthenticatedClient(ctx, &ts, cfg, &gemini.WebLoginOptions{
		NoBrowser: opts.NoBrowser,
		Prompt:    opts.Prompt,
	})
	if err != nil {
		return nil, fmt.Errorf("gemini authentication failed: %w", err)
	}

	// Skip onboarding here; rely on upstream configuration

	fileName := fmt.Sprintf("%s-%s.json", ts.Email, ts.ProjectID)
	metadata := map[string]any{
		"email":      ts.Email,
		"project_id": ts.ProjectID,
	}

	fmt.Println("Gemini authentication successful")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Storage:  &ts,
		Metadata: metadata,
	}, nil
}
