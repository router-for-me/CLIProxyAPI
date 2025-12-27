package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	// legacy client removed
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/oauthflow"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// CodexAuthenticator implements the OAuth login flow for Codex accounts.
type CodexAuthenticator struct {
	CallbackPort int
}

// NewCodexAuthenticator constructs a Codex authenticator with default settings.
func NewCodexAuthenticator() *CodexAuthenticator {
	return &CodexAuthenticator{CallbackPort: 1455}
}

func (a *CodexAuthenticator) Provider() string {
	return "codex"
}

func (a *CodexAuthenticator) RefreshLead() *time.Duration {
	d := 5 * 24 * time.Hour
	return &d
}

func (a *CodexAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
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
	authSvc := codex.NewCodexAuth(cfg)
	provider := codex.NewOAuthProvider(authSvc)
	flow, err := oauthflow.RunAuthCodeFlow(ctx, provider, oauthflow.AuthCodeFlowOptions{
		DesiredPort:  desiredPort,
		CallbackPath: "/auth/callback",
		Timeout:      5 * time.Minute,
		OnAuthURL: func(authURL string, callbackPort int, redirectURI string) {
			if desiredPort != 0 && callbackPort != desiredPort {
				log.Warnf("codex oauth callback port %d is busy; falling back to an ephemeral port", desiredPort)
			}

			if !opts.NoBrowser {
				fmt.Println("Opening browser for Codex authentication")
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

			fmt.Println("Waiting for Codex authentication callback...")
		},
	})
	if err != nil {
		var flowErr *oauthflow.FlowError
		if errors.As(err, &flowErr) && flowErr != nil {
			switch flowErr.Kind {
			case oauthflow.FlowErrorKindPortInUse:
				return nil, codex.NewAuthenticationError(codex.ErrPortInUse, err)
			case oauthflow.FlowErrorKindServerStartFailed:
				return nil, codex.NewAuthenticationError(codex.ErrServerStartFailed, err)
			case oauthflow.FlowErrorKindAuthorizeURLFailed:
				return nil, fmt.Errorf("codex authorization url generation failed: %w", flowErr.Err)
			case oauthflow.FlowErrorKindCallbackTimeout:
				return nil, codex.NewAuthenticationError(codex.ErrCallbackTimeout, err)
			case oauthflow.FlowErrorKindProviderError:
				code := strings.TrimSpace(flow.CallbackError)
				if code == "" {
					code = strings.TrimSpace(flowErr.Err.Error())
				}
				if code == "" {
					code = "oauth_error"
				}
				return nil, codex.NewOAuthError(code, "", http.StatusBadRequest)
			case oauthflow.FlowErrorKindInvalidState:
				return nil, codex.NewAuthenticationError(codex.ErrInvalidState, err)
			case oauthflow.FlowErrorKindCodeExchangeFailed:
				return nil, codex.NewAuthenticationError(codex.ErrCodeExchangeFailed, err)
			}
		}
		return nil, err
	}
	if flow == nil || flow.Token == nil {
		return nil, fmt.Errorf("codex authentication failed: missing token result")
	}

	email := ""
	accountID := ""
	if flow.Token.Metadata != nil {
		if raw, ok := flow.Token.Metadata["email"]; ok {
			if s, okStr := raw.(string); okStr {
				email = strings.TrimSpace(s)
			}
		}
		if raw, ok := flow.Token.Metadata["account_id"]; ok {
			if s, okStr := raw.(string); okStr {
				accountID = strings.TrimSpace(s)
			}
		}
	}
	if email == "" {
		return nil, fmt.Errorf("codex token storage missing account information")
	}

	tokenStorage := &codex.CodexTokenStorage{
		IDToken:      flow.Token.IDToken,
		AccessToken:  flow.Token.AccessToken,
		RefreshToken: flow.Token.RefreshToken,
		AccountID:    accountID,
		LastRefresh:  time.Now().Format(time.RFC3339),
		Email:        email,
		Expire:       flow.Token.ExpiresAt,
	}

	fileName := fmt.Sprintf("codex-%s.json", email)
	metadata := map[string]any{
		"email": email,
	}

	fmt.Println("Codex authentication successful")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}
