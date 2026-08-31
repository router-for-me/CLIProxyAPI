package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codebuddy"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// codebuddyRefreshLead is the duration before token expiry when refresh should occur.
var codebuddyRefreshLead = time.Hour

// CodeBuddyAuthenticator implements the state-based plugin auth login for
// CodeBuddy (WorkBuddy, Tencent copilot).
type CodeBuddyAuthenticator struct{}

// NewCodeBuddyAuthenticator constructs a new CodeBuddy authenticator.
func NewCodeBuddyAuthenticator() Authenticator {
	return &CodeBuddyAuthenticator{}
}

// Provider returns the provider key for codebuddy.
func (CodeBuddyAuthenticator) Provider() string {
	return "codebuddy-cn"
}

// RefreshLead returns the duration before token expiry when refresh should occur.
func (CodeBuddyAuthenticator) RefreshLead() *time.Duration {
	return &codebuddyRefreshLead
}

// Login runs the CodeBuddy state-based authorization flow: generate a state,
// open the authorization URL in a browser, then poll the token endpoint until
// the user completes authorization.
func (a CodeBuddyAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if opts == nil {
		opts = &LoginOptions{}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	client := codebuddy.NewClient(cfg, "")

	fmt.Println("Starting CodeBuddy CN (WorkBuddy) authentication...")
	state, err := client.FetchState(ctx)
	if err != nil {
		return nil, fmt.Errorf("codebuddy-cn: failed to generate auth state: %w", err)
	}

	fmt.Printf("\nTo authenticate, please visit:\n%s\n\n", state.AuthURL)
	if !opts.NoBrowser {
		if browser.IsAvailable() {
			if errOpen := browser.OpenURL(state.AuthURL); errOpen != nil {
				log.Warnf("Failed to open browser automatically: %v", errOpen)
			} else {
				fmt.Println("Browser opened automatically.")
			}
		}
	}

	fmt.Println("Waiting for authorization (complete the login in the browser)...")
	token, err := client.WaitForToken(ctx, state.State)
	if err != nil {
		return nil, fmt.Errorf("codebuddy-cn: %w", err)
	}

	// Account info is optional; the flow still succeeds without it.
	account, accErr := client.GetAccountInfo(ctx, token.AccessToken, state.State)
	if accErr != nil {
		log.Warnf("codebuddy-cn: failed to fetch account info: %v", accErr)
		account = nil
	}

	// Available models are optional; the executor refreshes them on token refresh.
	var models []codebuddy.ModelInfo
	configData, cfgErr := client.FetchConfig(ctx, token.AccessToken, accountUID(account))
	if cfgErr != nil {
		log.Warnf("codebuddy-cn: failed to fetch model config: %v", cfgErr)
	} else if parsed, parseErr := codebuddy.ParseModels(configData); parseErr != nil {
		log.Warnf("codebuddy-cn: failed to parse model config: %v", parseErr)
	} else {
		models = parsed
	}

	tokenStorage := codebuddy.BuildTokenStorage(token, account, models)

	metadata := map[string]any{
		"type":          "codebuddy-cn",
		"access_token":  tokenStorage.AccessToken,
		"refresh_token": tokenStorage.RefreshToken,
		"token_type":    tokenStorage.TokenType,
		"timestamp":     time.Now().UnixMilli(),
	}
	if tokenStorage.Scope != "" {
		metadata["scope"] = tokenStorage.Scope
	}
	if tokenStorage.Domain != "" {
		metadata["domain"] = tokenStorage.Domain
	}
	if tokenStorage.UID != "" {
		metadata["uid"] = tokenStorage.UID
	}
	if tokenStorage.Nickname != "" {
		metadata["nickname"] = tokenStorage.Nickname
	}
	if tokenStorage.Expired != "" {
		metadata["expired"] = tokenStorage.Expired
	}
	if len(tokenStorage.EnabledModels) > 0 {
		metadata["enabled_models"] = tokenStorage.EnabledModels
	}
	if tokenStorage.ModelsMeta != "" {
		metadata["models_meta"] = tokenStorage.ModelsMeta
	}

	fileName := fmt.Sprintf("codebuddy-cn-%d.json", time.Now().UnixMilli())

	label := "CodeBuddy CN User"
	if nickname := strings.TrimSpace(tokenStorage.Nickname); nickname != "" {
		label = nickname
	}

	fmt.Println("\nCodeBuddy CN authentication successful!")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    label,
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}

func accountUID(account *codebuddy.AccountInfo) string {
	if account == nil {
		return ""
	}
	return account.UID
}
