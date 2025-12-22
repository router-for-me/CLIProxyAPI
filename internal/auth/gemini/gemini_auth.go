// Package gemini provides authentication and token management functionality
// for Google's Gemini AI services. It handles OAuth2 authentication flows,
// including obtaining tokens via web-based authorization, storing tokens,
// and refreshing them when they expire.
package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/oauthflow"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/oauthhttp"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	geminiOauthClientID     = "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com"
	geminiOauthClientSecret = "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"
)

var (
	geminiOauthScopes = []string{
		"https://www.googleapis.com/auth/cloud-platform",
		"https://www.googleapis.com/auth/userinfo.email",
		"https://www.googleapis.com/auth/userinfo.profile",
	}
)

// GeminiAuth provides methods for handling the Gemini OAuth2 authentication flow.
// It encapsulates the logic for obtaining, storing, and refreshing authentication tokens
// for Google's Gemini AI services.
type GeminiAuth struct {
}

// NewGeminiAuth creates a new instance of GeminiAuth.
func NewGeminiAuth() *GeminiAuth {
	return &GeminiAuth{}
}

// GetAuthenticatedClient configures and returns an HTTP client ready for making authenticated API calls.
// It manages the entire OAuth2 flow, including handling proxies, loading existing tokens,
// initiating a new web-based OAuth flow if necessary, and refreshing tokens.
//
// Parameters:
//   - ctx: The context for the HTTP client
//   - ts: The Gemini token storage containing authentication tokens
//   - cfg: The configuration containing proxy settings
//   - noBrowser: Optional parameter to disable browser opening
//
// Returns:
//   - *http.Client: An HTTP client configured with authentication
//   - error: An error if the client configuration fails, nil otherwise
func (g *GeminiAuth) GetAuthenticatedClient(ctx context.Context, ts *GeminiTokenStorage, cfg *config.Config, noBrowser ...bool) (*http.Client, error) {
	oauthHTTPClient := util.SetOAuthProxy(&cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second})
	ctx = context.WithValue(ctx, oauth2.HTTPClient, oauthHTTPClient)

	// Configure the OAuth2 client.
	conf := &oauth2.Config{
		ClientID:     geminiOauthClientID,
		ClientSecret: geminiOauthClientSecret,
		RedirectURL:  "http://localhost:8085/oauth2callback", // This will be used by the local server.
		Scopes:       geminiOauthScopes,
		Endpoint:     google.Endpoint,
	}

	var token *oauth2.Token
	var err error

	// If no token is found in storage, initiate the web-based OAuth flow.
	if ts.Token == nil {
		fmt.Printf("Could not load token from file, starting OAuth flow.\n")
		token, err = g.getTokenFromWeb(ctx, oauthHTTPClient, noBrowser...)
		if err != nil {
			return nil, fmt.Errorf("failed to get token from web: %w", err)
		}
		// After getting a new token, create a new token storage object with user info.
		newTs, errCreateTokenStorage := g.createTokenStorage(ctx, conf, token, ts.ProjectID)
		if errCreateTokenStorage != nil {
			log.Errorf("Warning: failed to create token storage: %v", errCreateTokenStorage)
			return nil, errCreateTokenStorage
		}
		*ts = *newTs
	}

	// Unmarshal the stored token into an oauth2.Token object.
	tsToken, _ := json.Marshal(ts.Token)
	if err = json.Unmarshal(tsToken, &token); err != nil {
		return nil, fmt.Errorf("failed to unmarshal token: %w", err)
	}

	// Return an HTTP client that automatically handles token refreshing.
	return conf.Client(ctx, token), nil
}

// createTokenStorage creates a new GeminiTokenStorage object. It fetches the user's email
// using the provided token and populates the storage structure.
//
// Parameters:
//   - ctx: The context for the HTTP request
//   - config: The OAuth2 configuration
//   - token: The OAuth2 token to use for authentication
//   - projectID: The Google Cloud Project ID to associate with this token
//
// Returns:
//   - *GeminiTokenStorage: A new token storage object with user information
//   - error: An error if the token storage creation fails, nil otherwise
func (g *GeminiAuth) createTokenStorage(ctx context.Context, config *oauth2.Config, token *oauth2.Token, projectID string) (*GeminiTokenStorage, error) {
	httpClient := config.Client(ctx, token)
	status, _, bodyBytes, err := oauthhttp.Do(
		ctx,
		httpClient,
		func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.googleapis.com/oauth2/v1/userinfo?alt=json", nil)
			if err != nil {
				return nil, fmt.Errorf("could not get user info: %w", err)
			}
			req.Header.Set("Accept", "application/json")
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))
			return req, nil
		},
		oauthhttp.DefaultRetryConfig(),
	)
	if err != nil && status == 0 {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		msg := strings.TrimSpace(string(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("get user info request failed with status %d: %s: %w", status, msg, err)
		}
		return nil, fmt.Errorf("get user info request failed with status %d: %s", status, msg)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}

	emailResult := gjson.GetBytes(bodyBytes, "email")
	if emailResult.Exists() && emailResult.Type == gjson.String {
		fmt.Printf("Authenticated user email: %s\n", emailResult.String())
	} else {
		fmt.Println("Failed to get user email from token")
	}

	var ifToken map[string]any
	jsonData, _ := json.Marshal(token)
	err = json.Unmarshal(jsonData, &ifToken)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal token: %w", err)
	}

	ifToken["token_uri"] = "https://oauth2.googleapis.com/token"
	ifToken["client_id"] = geminiOauthClientID
	ifToken["client_secret"] = geminiOauthClientSecret
	ifToken["scopes"] = geminiOauthScopes
	ifToken["universe_domain"] = "googleapis.com"

	ts := GeminiTokenStorage{
		Token:     ifToken,
		ProjectID: projectID,
		Email:     emailResult.String(),
	}

	return &ts, nil
}

// getTokenFromWeb initiates the web-based OAuth2 authorization flow.
// It starts a local HTTP server to listen for the callback from Google's auth server,
// opens the user's browser to the authorization URL, and exchanges the received
// authorization code for an access token.
//
// Parameters:
//   - ctx: The context for the HTTP client
//   - config: The OAuth2 configuration
//   - noBrowser: Optional parameter to disable browser opening
//
// Returns:
//   - *oauth2.Token: The OAuth2 token obtained from the authorization flow
//   - error: An error if the token acquisition fails, nil otherwise
func (g *GeminiAuth) getTokenFromWeb(ctx context.Context, httpClient *http.Client, noBrowser ...bool) (*oauth2.Token, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	provider := NewOAuthProvider(httpClient)
	desiredPort := 8085

	flow, err := oauthflow.RunAuthCodeFlow(ctx, provider, oauthflow.AuthCodeFlowOptions{
		DesiredPort:  desiredPort,
		CallbackPath: "/oauth2callback",
		Timeout:      5 * time.Minute,
		OnAuthURL: func(authURL string, callbackPort int, redirectURI string) {
			if desiredPort != 0 && callbackPort != desiredPort {
				log.Warnf("gemini oauth: default port %d is busy, falling back to dynamic port", desiredPort)
			}

			opened := false
			if len(noBrowser) == 1 && !noBrowser[0] {
				fmt.Println("Opening browser for authentication...")
				if !browser.IsAvailable() {
					log.Warn("No browser available on this system")
					util.PrintSSHTunnelInstructions(callbackPort)
					fmt.Printf("Please manually open this URL in your browser:\n\n%s\n", authURL)
				} else if errOpen := browser.OpenURL(authURL); errOpen != nil {
					authErr := codex.NewAuthenticationError(codex.ErrBrowserOpenFailed, errOpen)
					log.Warn(codex.GetUserFriendlyMessage(authErr))
					util.PrintSSHTunnelInstructions(callbackPort)
					fmt.Printf("Please manually open this URL in your browser:\n\n%s\n", authURL)
					platformInfo := browser.GetPlatformInfo()
					log.Debugf("Browser platform info: %+v", platformInfo)
				} else {
					log.Debug("Browser opened successfully")
					opened = true
				}
			}

			if !opened {
				util.PrintSSHTunnelInstructions(callbackPort)
				fmt.Printf("Please open this URL in your browser:\n\n%s\n", authURL)
			}
			fmt.Println("Waiting for authentication callback...")
		},
	})
	if err != nil {
		var flowErr *oauthflow.FlowError
		if errors.As(err, &flowErr) && flowErr != nil {
			switch flowErr.Kind {
			case oauthflow.FlowErrorKindPortInUse:
				return nil, fmt.Errorf("gemini oauth callback port in use: %w", err)
			case oauthflow.FlowErrorKindServerStartFailed:
				return nil, fmt.Errorf("gemini oauth callback server failed: %w", err)
			case oauthflow.FlowErrorKindCallbackTimeout:
				return nil, fmt.Errorf("oauth flow timed out")
			case oauthflow.FlowErrorKindProviderError:
				return nil, fmt.Errorf("authentication failed via callback: %w", flowErr.Err)
			case oauthflow.FlowErrorKindInvalidState:
				return nil, fmt.Errorf("state mismatch in callback")
			case oauthflow.FlowErrorKindCodeExchangeFailed:
				return nil, fmt.Errorf("failed to exchange token: %w", flowErr.Err)
			}
		}
		return nil, err
	}
	if flow == nil || flow.Token == nil {
		return nil, fmt.Errorf("oauth flow failed: missing token result")
	}

	token := &oauth2.Token{
		AccessToken:  flow.Token.AccessToken,
		RefreshToken: flow.Token.RefreshToken,
		TokenType:    flow.Token.TokenType,
	}
	if strings.TrimSpace(flow.Token.ExpiresAt) != "" {
		if expiry, errParse := time.Parse(time.RFC3339, strings.TrimSpace(flow.Token.ExpiresAt)); errParse == nil {
			token.Expiry = expiry
		}
	}

	fmt.Println("Authentication successful.")
	return token, nil
}
