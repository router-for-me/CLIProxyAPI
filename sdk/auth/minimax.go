package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/minimax"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// minimaxRefreshLead is how long before expiry a token refresh is attempted.
var minimaxRefreshLead = 5 * time.Minute

// MinimaxAuthenticator implements the OAuth device flow login for MiniMax.
type MinimaxAuthenticator struct {
	region string
}

// NewMinimaxAuthenticator constructs a MiniMax authenticator for the global region.
func NewMinimaxAuthenticator() Authenticator {
	return &MinimaxAuthenticator{region: minimax.RegionGlobal}
}

// NewMinimaxCNAuthenticator constructs a MiniMax authenticator for the China region.
func NewMinimaxCNAuthenticator() Authenticator {
	return &MinimaxAuthenticator{region: minimax.RegionCN}
}

// Provider returns the provider key for the configured region. The two regions
// use separate keys so their credentials never share a provider, matching the
// kimi.com / kimi.ai split.
func (a MinimaxAuthenticator) Provider() string {
	return minimax.ProviderForRegion(a.region)
}

// RefreshLead returns the duration before token expiry when refresh should occur.
func (MinimaxAuthenticator) RefreshLead() *time.Duration { return &minimaxRefreshLead }

// Login initiates the MiniMax device flow authentication.
func (a MinimaxAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	region := minimax.NormalizeRegion(a.region)
	displayName := "MiniMax"
	if minimax.IsCNRegion(region) {
		displayName = "MiniMax (China)"
	}

	authSvc := minimax.NewMinimaxAuth(cfg, region)

	fmt.Printf("Starting %s authentication...\n", displayName)
	deviceCode, pkce, err := authSvc.StartDeviceFlow(ctx)
	if err != nil {
		return nil, fmt.Errorf("minimax: failed to start device flow: %w", err)
	}

	verificationURL := strings.TrimSpace(deviceCode.VerificationURI)
	fmt.Printf("\nTo authenticate, please visit:\n%s\n\n", verificationURL)
	if deviceCode.UserCode != "" {
		fmt.Printf("Then enter this code: %s\n\n", deviceCode.UserCode)
	}

	if !opts.NoBrowser {
		if browser.IsAvailable() {
			if errOpen := browser.OpenURL(verificationURL); errOpen != nil {
				log.Warnf("Failed to open browser automatically: %v", errOpen)
			} else {
				fmt.Println("Browser opened automatically.")
			}
		} else {
			log.Warn("No browser available; please open the URL manually")
		}
	}

	fmt.Println("Waiting for authorization...")

	authBundle, err := authSvc.WaitForAuthorization(ctx, deviceCode, pkce)
	if err != nil {
		return nil, fmt.Errorf("minimax: %w", err)
	}

	tokenStorage := authSvc.CreateTokenStorage(authBundle)
	if tokenStorage == nil || strings.TrimSpace(tokenStorage.AccessToken) == "" {
		return nil, fmt.Errorf("minimax: token storage missing access token")
	}

	// The executor reads base_url from Attributes, while file-backed credentials
	// reload with Metadata only. Record it in both.
	attributes := map[string]string{
		"base_url":    tokenStorage.BaseURL,
		"region":      region,
		"auth_kind":   coreauth.AuthKindOAuth,
		"claude_base": minimax.ResolveClaudeBaseURL(region),
	}

	metadata := map[string]any{
		"type":         "minimax",
		"auth_kind":    coreauth.AuthKindOAuth,
		"access_token": tokenStorage.AccessToken,
		"region":       region,
		"base_url":     tokenStorage.BaseURL,
		"timestamp":    time.Now().UnixMilli(),
	}
	if tokenStorage.RefreshToken != "" {
		metadata["refresh_token"] = tokenStorage.RefreshToken
	}
	if tokenStorage.TokenType != "" {
		metadata["token_type"] = tokenStorage.TokenType
	}
	if tokenStorage.Scope != "" {
		metadata["scope"] = tokenStorage.Scope
	}
	if tokenStorage.ResourceURL != "" {
		metadata["resource_url"] = tokenStorage.ResourceURL
	}
	if tokenStorage.Expired != "" {
		metadata["expired"] = tokenStorage.Expired
	}
	metadata["last_refresh"] = tokenStorage.LastRefresh

	fileName := fmt.Sprintf("minimax-%d.json", time.Now().UnixMilli())

	fmt.Printf("\n%s authentication successful!\n", displayName)

	return &coreauth.Auth{
		ID:         fileName,
		Provider:   a.Provider(),
		FileName:   fileName,
		Label:      displayName,
		Storage:    tokenStorage,
		Metadata:   metadata,
		Attributes: attributes,
	}, nil
}

const regionMetadataKey = "minimax_region"
