package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/iflow"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/oauthflow"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// IFlowAuthenticator implements the OAuth login flow for iFlow accounts.
type IFlowAuthenticator struct{}

// NewIFlowAuthenticator constructs a new authenticator instance.
func NewIFlowAuthenticator() *IFlowAuthenticator { return &IFlowAuthenticator{} }

// Provider returns the provider key for the authenticator.
func (a *IFlowAuthenticator) Provider() string { return "iflow" }

// RefreshLead indicates how soon before expiry a refresh should be attempted.
func (a *IFlowAuthenticator) RefreshLead() *time.Duration {
	d := 24 * time.Hour
	return &d
}

// Login performs the OAuth code flow using a local callback server.
func (a *IFlowAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	authSvc := iflow.NewIFlowAuth(cfg)
	desiredPort := iflow.CallbackPort
	provider := iflow.NewOAuthProvider(authSvc)

	flow, err := oauthflow.RunAuthCodeFlow(ctx, provider, oauthflow.AuthCodeFlowOptions{
		DesiredPort:  desiredPort,
		CallbackPath: "/oauth2callback",
		Timeout:      5 * time.Minute,
		OnAuthURL: func(authURL string, callbackPort int, redirectURI string) {
			if desiredPort != 0 && callbackPort != desiredPort {
				log.Warnf("iflow oauth callback port %d is busy; falling back to an ephemeral port", desiredPort)
			}

			if !opts.NoBrowser {
				fmt.Println("Opening browser for iFlow authentication")
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

			fmt.Println("Waiting for iFlow authentication callback...")
		},
	})
	if err != nil {
		var flowErr *oauthflow.FlowError
		if errors.As(err, &flowErr) && flowErr != nil {
			switch flowErr.Kind {
			case oauthflow.FlowErrorKindPortInUse:
				return nil, fmt.Errorf("iflow authentication server port in use: %w", err)
			case oauthflow.FlowErrorKindServerStartFailed:
				return nil, fmt.Errorf("iflow authentication server failed: %w", err)
			case oauthflow.FlowErrorKindCallbackTimeout:
				return nil, fmt.Errorf("iflow auth: callback wait failed: %w", err)
			case oauthflow.FlowErrorKindProviderError:
				if flow != nil && flow.CallbackError != "" {
					return nil, fmt.Errorf("iflow auth: provider returned error %s", flow.CallbackError)
				}
				return nil, fmt.Errorf("iflow auth: provider returned error")
			case oauthflow.FlowErrorKindInvalidState:
				return nil, fmt.Errorf("iflow auth: state mismatch")
			case oauthflow.FlowErrorKindCodeExchangeFailed:
				return nil, fmt.Errorf("iflow authentication failed: %w", flowErr.Err)
			}
		}
		return nil, err
	}
	if flow == nil || flow.Token == nil {
		return nil, fmt.Errorf("iflow authentication failed: missing token result")
	}

	email := ""
	apiKey := ""
	if flow.Token.Metadata != nil {
		if raw, ok := flow.Token.Metadata["email"]; ok {
			if s, okStr := raw.(string); okStr {
				email = strings.TrimSpace(s)
			}
		}
		if raw, ok := flow.Token.Metadata["api_key"]; ok {
			if s, okStr := raw.(string); okStr {
				apiKey = strings.TrimSpace(s)
			}
		}
	}
	if email == "" {
		return nil, fmt.Errorf("iflow authentication failed: missing account identifier")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("iflow authentication failed: missing api key")
	}

	tokenStorage := &iflow.IFlowTokenStorage{
		AccessToken:  flow.Token.AccessToken,
		RefreshToken: flow.Token.RefreshToken,
		LastRefresh:  time.Now().Format(time.RFC3339),
		Expire:       flow.Token.ExpiresAt,
		APIKey:       apiKey,
		Email:        email,
		TokenType:    flow.Token.TokenType,
	}

	fileName := fmt.Sprintf("iflow-%s-%d.json", email, time.Now().Unix())
	metadata := map[string]any{
		"email":         email,
		"api_key":       apiKey,
		"access_token":  tokenStorage.AccessToken,
		"refresh_token": tokenStorage.RefreshToken,
		"expired":       tokenStorage.Expire,
	}

	fmt.Println("iFlow authentication successful")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Storage:  tokenStorage,
		Metadata: metadata,
		Attributes: map[string]string{
			"api_key": apiKey,
		},
	}, nil
}
