package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	// legacy client removed
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/oauthflow"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// ClaudeAuthenticator implements the OAuth login flow for Anthropic Claude accounts.
type ClaudeAuthenticator struct {
	CallbackPort int
}

// NewClaudeAuthenticator constructs a Claude authenticator with default settings.
func NewClaudeAuthenticator() *ClaudeAuthenticator {
	return &ClaudeAuthenticator{CallbackPort: 54545}
}

func (a *ClaudeAuthenticator) Provider() string {
	return "claude"
}

func (a *ClaudeAuthenticator) RefreshLead() *time.Duration {
	d := 4 * time.Hour
	return &d
}

func (a *ClaudeAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	desiredPort := a.CallbackPort
	authSvc := claude.NewClaudeAuth(cfg)
	provider := claude.NewOAuthProvider(authSvc)

	flow, err := oauthflow.RunAuthCodeFlow(ctx, provider, oauthflow.AuthCodeFlowOptions{
		DesiredPort:  desiredPort,
		CallbackPath: "/callback",
		Timeout:      5 * time.Minute,
		OnAuthURL: func(authURL string, callbackPort int, redirectURI string) {
			if desiredPort != 0 && callbackPort != desiredPort {
				log.Warnf("claude oauth callback port %d is busy; falling back to an ephemeral port", desiredPort)
			}

			if !opts.NoBrowser {
				fmt.Println("Opening browser for Claude authentication")
				if !browser.IsAvailable() {
					log.Warn("No browser available; please open the URL manually")
					util.PrintSSHTunnelInstructions(callbackPort)
					fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
				} else if errOpen := browser.OpenURL(authURL); errOpen != nil {
					log.Warnf("Failed to open browser automatically: %v", errOpen)
					util.PrintSSHTunnelInstructions(callbackPort)
					fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
				}
			} else {
				util.PrintSSHTunnelInstructions(callbackPort)
				fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
			}

			fmt.Println("Waiting for Claude authentication callback...")
		},
	})
	if err != nil {
		var flowErr *oauthflow.FlowError
		if errors.As(err, &flowErr) && flowErr != nil {
			switch flowErr.Kind {
			case oauthflow.FlowErrorKindPortInUse:
				return nil, claude.NewAuthenticationError(claude.ErrPortInUse, err)
			case oauthflow.FlowErrorKindServerStartFailed:
				return nil, claude.NewAuthenticationError(claude.ErrServerStartFailed, err)
			case oauthflow.FlowErrorKindAuthorizeURLFailed:
				return nil, fmt.Errorf("claude authorization url generation failed: %w", flowErr.Err)
			case oauthflow.FlowErrorKindCallbackTimeout:
				return nil, claude.NewAuthenticationError(claude.ErrCallbackTimeout, err)
			case oauthflow.FlowErrorKindProviderError:
				code := strings.TrimSpace(flow.CallbackError)
				if code == "" {
					code = strings.TrimSpace(flowErr.Err.Error())
				}
				if code == "" {
					code = "oauth_error"
				}
				return nil, claude.NewOAuthError(code, "", http.StatusBadRequest)
			case oauthflow.FlowErrorKindInvalidState:
				return nil, claude.NewAuthenticationError(claude.ErrInvalidState, err)
			case oauthflow.FlowErrorKindCodeExchangeFailed:
				return nil, claude.NewAuthenticationError(claude.ErrCodeExchangeFailed, err)
			}
		}
		return nil, err
	}
	if flow == nil || flow.Token == nil {
		return nil, fmt.Errorf("claude authentication failed: missing token result")
	}

	email := ""
	if flow.Token.Metadata != nil {
		if raw, ok := flow.Token.Metadata["email"]; ok {
			if s, okStr := raw.(string); okStr {
				email = strings.TrimSpace(s)
			}
		}
	}
	if email == "" {
		return nil, fmt.Errorf("claude token storage missing account information")
	}

	tokenStorage := &claude.ClaudeTokenStorage{
		AccessToken:  flow.Token.AccessToken,
		RefreshToken: flow.Token.RefreshToken,
		LastRefresh:  time.Now().Format(time.RFC3339),
		Email:        email,
		Expire:       flow.Token.ExpiresAt,
	}

	fileName := fmt.Sprintf("claude-%s.json", email)
	metadata := map[string]any{
		"email": email,
	}

	fmt.Println("Claude authentication successful")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}
