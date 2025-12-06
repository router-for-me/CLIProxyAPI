package auth

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// CopilotAuthenticator implements GitHub Copilot OAuth (device flow).
type CopilotAuthenticator struct{}

const (
	defaultCopilotClientID = "01ab8ac9400c4e429b23" // public VS Code GitHub client id
	copilotScope           = "read:user user:email copilot"
)

// NewCopilotAuthenticator constructs a Copilot authenticator.
func NewCopilotAuthenticator() *CopilotAuthenticator { return &CopilotAuthenticator{} }

// Provider returns the provider key.
func (a *CopilotAuthenticator) Provider() string { return "copilot" }

// RefreshLead returns nil (no refresh flow supported yet).
func (a *CopilotAuthenticator) RefreshLead() *time.Duration { return nil }

// Login executes the GitHub device flow to obtain a Copilot token.
func (a *CopilotAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	clientID := strings.TrimSpace(os.Getenv("COPILOT_CLIENT_ID"))
	if clientID == "" {
		clientID = defaultCopilotClientID
	}

	scope := copilotScope
	dc, err := copilot.StartDeviceFlow(ctx, clientID, scope)
	if err != nil {
		return nil, err
	}

	fmt.Printf("\nTo authenticate Copilot, visit:\n  %s\nthen enter code: %s\n\n", dc.VerificationURI, dc.UserCode)
	if dc.VerificationURIComplete != "" {
		fmt.Printf("Direct link: %s\n\n", dc.VerificationURIComplete)
	}

	pollCtx, cancel := context.WithTimeout(ctx, time.Duration(dc.ExpiresIn)*time.Second)
	defer cancel()

	interval := time.Duration(dc.Interval) * time.Second
	token, err := copilot.PollForToken(pollCtx, clientID, dc.DeviceCode, interval)
	if err != nil {
		return nil, err
	}

	user, err := copilot.FetchUser(ctx, token.AccessToken)
	if err != nil {
		return nil, err
	}

	expiresAt := time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)

	fileName := fmt.Sprintf("copilot-%s.json", strings.TrimSpace(user.Login))
	storage := &copilot.TokenStorage{
		AccessToken: token.AccessToken,
		TokenType:   token.TokenType,
		Scope:       token.Scope,
		Expired:     expiresAt,
		Email:       user.Email,
		Login:       user.Login,
	}

	metadata := map[string]any{"email": user.Email, "login": user.Login}
	attributes := map[string]string{
		"base_url":     "https://api.githubcopilot.com",
		"api_key":      token.AccessToken,
		"provider_key": "copilot",
		"compat_name":  "copilot",
		// Required headers for GitHub Copilot API
		"header:Editor-Version":         "vscode/1.96.0",
		"header:Editor-Plugin-Version":  "copilot-chat/0.24.0",
		"header:Copilot-Integration-Id": "vscode-chat",
		"header:User-Agent":             "GitHubCopilotChat/0.24.0",
	}

	now := time.Now().UTC()
	return &coreauth.Auth{
		ID:            fileName,
		Provider:      a.Provider(),
		FileName:      fileName,
		Storage:       storage,
		Metadata:      metadata,
		Attributes:    attributes,
		Label:         user.Login,
		CreatedAt:     now,
		UpdatedAt:     now,
		Status:        coreauth.StatusActive,
		StatusMessage: "authenticated",
	}, nil
}
