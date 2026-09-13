package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codebuddycn"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

var codeBuddyCNRefreshLead = 5 * time.Minute

// CodeBuddyCNAuthenticator implements CodeBuddy CN's browser polling flow.
type CodeBuddyCNAuthenticator struct{}

// NewCodeBuddyCNAuthenticator constructs a CodeBuddy CN authenticator.
func NewCodeBuddyCNAuthenticator() Authenticator { return &CodeBuddyCNAuthenticator{} }

// Provider returns the CodeBuddy CN provider key.
func (CodeBuddyCNAuthenticator) Provider() string { return "codebuddy-cn" }

// RefreshLead instructs the runtime to refresh shortly before expiry.
func (CodeBuddyCNAuthenticator) RefreshLead() *time.Duration { return &codeBuddyCNRefreshLead }

// Login starts browser authorization and waits for CodeBuddy to issue tokens.
func (a CodeBuddyCNAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}
	client := codebuddycn.NewClient(cfg)
	fmt.Println("Starting CodeBuddy CN authentication...")
	device, err := client.StartDeviceFlow(ctx)
	if err != nil {
		return nil, err
	}
	fmt.Printf("\nTo authenticate, please visit:\n%s\n\n", device.AuthURL)
	if !opts.NoBrowser && browser.IsAvailable() {
		if errOpen := browser.OpenURL(device.AuthURL); errOpen != nil {
			log.Warnf("Failed to open browser automatically: %v", errOpen)
		} else {
			fmt.Println("Browser opened automatically.")
		}
	}
	fmt.Println("Waiting for authorization...")
	token, err := client.WaitForAuthorization(ctx, device)
	if err != nil {
		return nil, err
	}
	fileName := fmt.Sprintf("codebuddy-cn-%d.json", time.Now().UnixMilli())
	metadata := tokenMetadata(token)
	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    "CodeBuddy CN",
		Metadata: metadata,
		Attributes: map[string]string{
			coreauth.AttributeAuthKind: coreauth.AuthKindOAuth,
			"base_url":                 codebuddycn.APIBaseURL,
		},
	}, nil
}

func tokenMetadata(token *codebuddycn.TokenData) map[string]any {
	metadata := map[string]any{
		"type":          "codebuddy-cn",
		"auth_kind":     "oauth",
		"access_token":  token.AccessToken,
		"refresh_token": token.RefreshToken,
		"token_type":    token.TokenType,
		"expires_in":    token.ExpiresIn,
		"base_url":      codebuddycn.APIBaseURL,
		"timestamp":     time.Now().UnixMilli(),
	}
	if !token.ExpiresAt.IsZero() {
		metadata["expired"] = token.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if strings.TrimSpace(token.RefreshToken) == "" {
		delete(metadata, "refresh_token")
	}
	return metadata
}
