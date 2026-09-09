package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type CopilotAuthenticator struct{}

func NewCopilotAuthenticator() Authenticator             { return &CopilotAuthenticator{} }
func (CopilotAuthenticator) Provider() string            { return copilot.Provider }
func (CopilotAuthenticator) RefreshLead() *time.Duration { lead := copilot.RefreshLead; return &lead }

func (CopilotAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("github-copilot: configuration is required")
	}
	client := copilot.NewClient(cfg, "")
	code, err := client.StartDeviceFlow(ctx)
	if err != nil {
		return nil, err
	}
	fmt.Printf("To sign in to GitHub Copilot, visit %s and enter code %s\n", code.VerificationURI, code.UserCode)
	if (opts == nil || !opts.NoBrowser) && browser.IsAvailable() {
		if errOpen := browser.OpenURL(code.VerificationURI); errOpen != nil {
			log.WithError(errOpen).Warn("github-copilot: could not open browser")
		}
	}
	githubToken, err := client.WaitForAuthorization(ctx, code)
	if err != nil {
		return nil, err
	}
	metadata, err := client.Login(ctx, githubToken)
	if err != nil {
		return nil, err
	}
	return NewCopilotAuthRecord(metadata), nil
}

// NewCopilotAuthRecord uses GitHub's stable account ID so signing in again updates
// the existing credential while separate accounts remain available for rotation.
func NewCopilotAuthRecord(metadata map[string]any) *coreauth.Auth {
	name := fmt.Sprintf("github-copilot-%v.json", metadata["github_id"])
	label, _ := metadata["github_login"].(string)
	return &coreauth.Auth{ID: name, FileName: name, Provider: copilot.Provider, Label: label, Metadata: metadata}
}
