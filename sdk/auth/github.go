package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	githubauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/github"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// githubRefreshLead is how early before Copilot token expiry we trigger a refresh.
var githubRefreshLead = 5 * time.Minute

// GithubCopilotAuthenticator implements the OAuth device flow login for GitHub Copilot.
type GithubCopilotAuthenticator struct{}

// NewGithubCopilotAuthenticator constructs a new GitHub Copilot authenticator.
func NewGithubCopilotAuthenticator() Authenticator {
	return &GithubCopilotAuthenticator{}
}

// Provider returns the provider key for GitHub Copilot.
func (GithubCopilotAuthenticator) Provider() string {
	return "github-copilot"
}

// RefreshLead returns the duration before Copilot token expiry when refresh should occur.
// Copilot tokens are short-lived (~30 minutes), so we refresh 5 minutes early.
func (GithubCopilotAuthenticator) RefreshLead() *time.Duration {
	return &githubRefreshLead
}

// Login initiates the GitHub device flow to obtain a GitHub user token, then
// exchanges it for a Copilot API token. Both tokens are persisted for future use.
func (a GithubCopilotAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	authSvc := githubauth.NewGithubAuth(cfg)

	fmt.Println("Starting GitHub Copilot authentication...")
	deviceCode, err := authSvc.RequestDeviceCode(ctx)
	if err != nil {
		return nil, fmt.Errorf("github-copilot: failed to start device flow: %w", err)
	}

	verificationURL := deviceCode.VerificationURIComplete
	if verificationURL == "" {
		verificationURL = deviceCode.VerificationURI
	}

	fmt.Printf("\nTo authenticate, please visit:\n%s\n\n", verificationURL)
	if deviceCode.UserCode != "" {
		fmt.Printf("Enter code: %s\n\n", deviceCode.UserCode)
	}

	if !opts.NoBrowser {
		if browser.IsAvailable() {
			if errOpen := browser.OpenURL(verificationURL); errOpen != nil {
				log.Warnf("github-copilot: failed to open browser automatically: %v", errOpen)
			} else {
				fmt.Println("Browser opened automatically.")
			}
		}
	}

	fmt.Println("Waiting for GitHub authorization...")
	if deviceCode.ExpiresIn > 0 {
		fmt.Printf("(Times out in %d seconds if not authorized)\n", deviceCode.ExpiresIn)
	}

	githubToken, err := authSvc.PollForGithubToken(ctx, deviceCode)
	if err != nil {
		return nil, fmt.Errorf("github-copilot: %w", err)
	}
	githubToken = strings.TrimSpace(githubToken)
	if githubToken == "" {
		return nil, fmt.Errorf("github-copilot: received empty GitHub token")
	}

	login, email, err := authSvc.FetchUserInfo(ctx, githubToken)
	if err != nil {
		return nil, fmt.Errorf("github-copilot: failed to fetch user info: %w", err)
	}

	copilotToken, err := authSvc.FetchCopilotToken(ctx, githubToken)
	if err != nil {
		return nil, fmt.Errorf("github-copilot: failed to fetch Copilot token: %w", err)
	}

	tokenStorage := &githubauth.GithubTokenStorage{
		GithubToken: githubToken,
		AccessToken: copilotToken.Token,
		Login:       login,
		Email:       email,
		Expired:     copilotToken.ExpiresAt.String(),
	}

	metadata := map[string]any{
		"type":         "github-copilot",
		"github_token": githubToken,
		"access_token": copilotToken.Token,
		"login":        login,
		"timestamp":    time.Now().UnixMilli(),
	}
	if email != "" {
		metadata["email"] = email
	}
	if copilotToken.ExpiresAt.String() != "" {
		metadata["expired"] = copilotToken.ExpiresAt.String()
	}

	fileName := githubauth.CredentialFileName(login)
	label := login
	if label == "" {
		label = "github-copilot"
	}

	fmt.Println("\nGitHub Copilot authentication successful!")
	fmt.Printf("Authenticated as: %s\n", label)

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    label,
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}
