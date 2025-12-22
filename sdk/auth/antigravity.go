package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/oauthflow"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/oauthhttp"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	antigravityClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	antigravityClientSecret = "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"
	antigravityCallbackPort = 51121
)

var antigravityScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

// AntigravityAuthenticator implements OAuth login for the antigravity provider.
type AntigravityAuthenticator struct{}

// NewAntigravityAuthenticator constructs a new authenticator instance.
func NewAntigravityAuthenticator() Authenticator { return &AntigravityAuthenticator{} }

// Provider returns the provider key for antigravity.
func (AntigravityAuthenticator) Provider() string { return "antigravity" }

// RefreshLead instructs the manager to refresh five minutes before expiry.
func (AntigravityAuthenticator) RefreshLead() *time.Duration {
	lead := 5 * time.Minute
	return &lead
}

// Login launches a local OAuth flow to obtain antigravity tokens and persists them.
func (AntigravityAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	httpClient := util.SetOAuthProxy(&cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second})

	desiredPort := antigravityCallbackPort
	provider := newAntigravityOAuthProvider(httpClient)

	flow, err := oauthflow.RunAuthCodeFlow(ctx, provider, oauthflow.AuthCodeFlowOptions{
		DesiredPort:  desiredPort,
		CallbackPath: "/oauth-callback",
		Timeout:      5 * time.Minute,
		OnAuthURL: func(authURL string, callbackPort int, redirectURI string) {
			if desiredPort != 0 && callbackPort != desiredPort {
				log.Warnf("antigravity oauth callback port %d is busy; falling back to an ephemeral port", desiredPort)
			}

			if !opts.NoBrowser {
				fmt.Println("Opening browser for antigravity authentication")
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

			fmt.Println("Waiting for antigravity authentication callback...")
		},
	})
	if err != nil {
		var flowErr *oauthflow.FlowError
		if errors.As(err, &flowErr) && flowErr != nil {
			switch flowErr.Kind {
			case oauthflow.FlowErrorKindPortInUse:
				return nil, fmt.Errorf("antigravity auth callback port in use: %w", err)
			case oauthflow.FlowErrorKindServerStartFailed:
				return nil, fmt.Errorf("antigravity auth callback server failed: %w", err)
			case oauthflow.FlowErrorKindCallbackTimeout:
				return nil, fmt.Errorf("antigravity auth: callback wait failed: %w", err)
			case oauthflow.FlowErrorKindProviderError:
				if flow != nil && flow.CallbackError != "" {
					return nil, fmt.Errorf("antigravity auth: provider returned error %s", flow.CallbackError)
				}
				return nil, fmt.Errorf("antigravity auth: provider returned error")
			case oauthflow.FlowErrorKindInvalidState:
				return nil, fmt.Errorf("antigravity auth: state mismatch")
			case oauthflow.FlowErrorKindCodeExchangeFailed:
				return nil, fmt.Errorf("antigravity token exchange failed: %w", flowErr.Err)
			}
		}
		return nil, err
	}
	if flow == nil || flow.Token == nil {
		return nil, fmt.Errorf("antigravity authentication failed: missing token result")
	}

	token := flow.Token

	email := ""
	if token.AccessToken != "" {
		if info, errInfo := fetchAntigravityUserInfo(ctx, token.AccessToken, httpClient); errInfo == nil && strings.TrimSpace(info.Email) != "" {
			email = strings.TrimSpace(info.Email)
		}
	}

	// Fetch project ID via loadCodeAssist (same approach as Gemini CLI)
	projectID := ""
	if token.AccessToken != "" {
		fetchedProjectID, errProject := fetchAntigravityProjectID(ctx, token.AccessToken, httpClient)
		if errProject != nil {
			log.Warnf("antigravity: failed to fetch project ID: %v", errProject)
		} else {
			projectID = fetchedProjectID
			log.Infof("antigravity: obtained project ID %s", projectID)
		}
	}

	now := time.Now()
	expiresIn := int64(0)
	if token.Metadata != nil {
		switch v := token.Metadata["expires_in"].(type) {
		case int:
			expiresIn = int64(v)
		case int64:
			expiresIn = v
		case float64:
			expiresIn = int64(v)
		}
	}
	expiredAt := strings.TrimSpace(token.ExpiresAt)
	if expiredAt == "" && expiresIn > 0 {
		expiredAt = now.Add(time.Duration(expiresIn) * time.Second).Format(time.RFC3339)
	}
	metadata := map[string]any{
		"type":          "antigravity",
		"access_token":  token.AccessToken,
		"refresh_token": token.RefreshToken,
		"expires_in":    expiresIn,
		"timestamp":     now.UnixMilli(),
		"expired":       expiredAt,
	}
	if email != "" {
		metadata["email"] = email
	}
	if projectID != "" {
		metadata["project_id"] = projectID
	}

	fileName := sanitizeAntigravityFileName(email)
	label := email
	if label == "" {
		label = "antigravity"
	}

	fmt.Println("Antigravity authentication successful")
	if projectID != "" {
		fmt.Printf("Using GCP project: %s\n", projectID)
	}
	return &coreauth.Auth{
		ID:       fileName,
		Provider: "antigravity",
		FileName: fileName,
		Label:    label,
		Metadata: metadata,
	}, nil
}

type antigravityOAuthProvider struct {
	httpClient *http.Client
}

func newAntigravityOAuthProvider(httpClient *http.Client) *antigravityOAuthProvider {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &antigravityOAuthProvider{httpClient: httpClient}
}

func (p *antigravityOAuthProvider) Provider() string {
	return "antigravity"
}

func (p *antigravityOAuthProvider) AuthorizeURL(session oauthflow.OAuthSession) (string, oauthflow.OAuthSession, error) {
	if p == nil {
		return "", session, fmt.Errorf("antigravity oauth provider: provider is nil")
	}
	redirectURI := strings.TrimSpace(session.RedirectURI)
	if redirectURI == "" {
		return "", session, fmt.Errorf("antigravity oauth provider: redirect URI is empty")
	}
	authURL := buildAntigravityAuthURL(redirectURI, session.State, session.CodeChallenge)
	return authURL, session, nil
}

func (p *antigravityOAuthProvider) ExchangeCode(ctx context.Context, session oauthflow.OAuthSession, code string) (*oauthflow.TokenResult, error) {
	if p == nil {
		return nil, fmt.Errorf("antigravity oauth provider: provider is nil")
	}
	tokenResp, err := exchangeAntigravityCode(ctx, code, session.RedirectURI, session.CodeVerifier, p.httpClient)
	if err != nil {
		return nil, err
	}
	if tokenResp == nil {
		return nil, fmt.Errorf("antigravity oauth provider: token response is nil")
	}

	tokenType := strings.TrimSpace(tokenResp.TokenType)
	if tokenType == "" {
		tokenType = "Bearer"
	}
	expiresAt := ""
	if tokenResp.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339)
	}

	meta := map[string]any{
		"expires_in": tokenResp.ExpiresIn,
	}

	return &oauthflow.TokenResult{
		AccessToken:  strings.TrimSpace(tokenResp.AccessToken),
		RefreshToken: strings.TrimSpace(tokenResp.RefreshToken),
		ExpiresAt:    expiresAt,
		TokenType:    tokenType,
		Metadata:     meta,
	}, nil
}

func (p *antigravityOAuthProvider) Refresh(ctx context.Context, refreshToken string) (*oauthflow.TokenResult, error) {
	if p == nil {
		return nil, fmt.Errorf("antigravity oauth provider: provider is nil")
	}
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, fmt.Errorf("antigravity oauth provider: refresh token is empty")
	}
	tokenResp, err := refreshAntigravityTokens(ctx, refreshToken, p.httpClient)
	if err != nil {
		return nil, err
	}
	if tokenResp == nil {
		return nil, fmt.Errorf("antigravity oauth provider: refresh response is nil")
	}

	tokenType := strings.TrimSpace(tokenResp.TokenType)
	if tokenType == "" {
		tokenType = "Bearer"
	}
	expiresAt := ""
	if tokenResp.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339)
	}

	meta := map[string]any{
		"expires_in": tokenResp.ExpiresIn,
	}

	return &oauthflow.TokenResult{
		AccessToken:  strings.TrimSpace(tokenResp.AccessToken),
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
		TokenType:    tokenType,
		Metadata:     meta,
	}, nil
}

func (p *antigravityOAuthProvider) Revoke(ctx context.Context, token string) error {
	return oauthflow.ErrRevokeNotSupported
}

type antigravityTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

func exchangeAntigravityCode(ctx context.Context, code, redirectURI, codeVerifier string, httpClient *http.Client) (*antigravityTokenResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	data := url.Values{}
	data.Set("code", code)
	data.Set("client_id", antigravityClientID)
	data.Set("client_secret", antigravityClientSecret)
	data.Set("redirect_uri", redirectURI)
	data.Set("grant_type", "authorization_code")
	if strings.TrimSpace(codeVerifier) != "" {
		data.Set("code_verifier", strings.TrimSpace(codeVerifier))
	}

	encoded := data.Encode()
	status, _, body, err := oauthhttp.Do(
		ctx,
		httpClient,
		func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(encoded))
			if err != nil {
				return nil, err
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Accept", "application/json")
			return req, nil
		},
		oauthhttp.DefaultRetryConfig(),
	)
	if err != nil && status == 0 {
		return nil, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		msg := strings.TrimSpace(string(body))
		if err != nil {
			return nil, fmt.Errorf("oauth token exchange failed: status %d: %s: %w", status, msg, err)
		}
		return nil, fmt.Errorf("oauth token exchange failed: status %d: %s", status, msg)
	}
	if err != nil {
		return nil, err
	}

	var token antigravityTokenResponse
	if errDecode := json.Unmarshal(body, &token); errDecode != nil {
		return nil, errDecode
	}
	return &token, nil
}

func refreshAntigravityTokens(ctx context.Context, refreshToken string, httpClient *http.Client) (*antigravityTokenResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token is empty")
	}
	data := url.Values{}
	data.Set("refresh_token", refreshToken)
	data.Set("client_id", antigravityClientID)
	data.Set("client_secret", antigravityClientSecret)
	data.Set("grant_type", "refresh_token")

	encoded := data.Encode()
	status, _, body, err := oauthhttp.Do(
		ctx,
		httpClient,
		func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(encoded))
			if err != nil {
				return nil, err
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Accept", "application/json")
			return req, nil
		},
		oauthhttp.DefaultRetryConfig(),
	)
	if err != nil && status == 0 {
		return nil, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		msg := strings.TrimSpace(string(body))
		if err != nil {
			return nil, fmt.Errorf("oauth token refresh failed: status %d: %s: %w", status, msg, err)
		}
		return nil, fmt.Errorf("oauth token refresh failed: status %d: %s", status, msg)
	}
	if err != nil {
		return nil, err
	}

	var token antigravityTokenResponse
	if errDecode := json.Unmarshal(body, &token); errDecode != nil {
		return nil, errDecode
	}
	return &token, nil
}

type antigravityUserInfo struct {
	Email string `json:"email"`
}

func fetchAntigravityUserInfo(ctx context.Context, accessToken string, httpClient *http.Client) (*antigravityUserInfo, error) {
	if strings.TrimSpace(accessToken) == "" {
		return &antigravityUserInfo{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	status, _, body, err := oauthhttp.Do(
		ctx,
		httpClient,
		func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.googleapis.com/oauth2/v1/userinfo?alt=json", nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "Bearer "+accessToken)
			req.Header.Set("Accept", "application/json")
			return req, nil
		},
		oauthhttp.DefaultRetryConfig(),
	)
	if err != nil && status == 0 {
		return nil, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return &antigravityUserInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	var info antigravityUserInfo
	if errDecode := json.Unmarshal(body, &info); errDecode != nil {
		return nil, errDecode
	}
	return &info, nil
}

func buildAntigravityAuthURL(redirectURI, state, codeChallenge string) string {
	params := url.Values{}
	params.Set("access_type", "offline")
	params.Set("client_id", antigravityClientID)
	params.Set("prompt", "consent")
	params.Set("redirect_uri", redirectURI)
	params.Set("response_type", "code")
	params.Set("scope", strings.Join(antigravityScopes, " "))
	params.Set("state", state)
	if strings.TrimSpace(codeChallenge) != "" {
		params.Set("code_challenge", strings.TrimSpace(codeChallenge))
		params.Set("code_challenge_method", "S256")
	}
	return "https://accounts.google.com/o/oauth2/v2/auth?" + params.Encode()
}

func sanitizeAntigravityFileName(email string) string {
	if strings.TrimSpace(email) == "" {
		return "antigravity.json"
	}
	replacer := strings.NewReplacer("@", "_", ".", "_")
	return fmt.Sprintf("antigravity-%s.json", replacer.Replace(email))
}

// Antigravity API constants for project discovery
const (
	antigravityAPIEndpoint    = "https://cloudcode-pa.googleapis.com"
	antigravityAPIVersion     = "v1internal"
	antigravityAPIUserAgent   = "google-api-nodejs-client/9.15.1"
	antigravityAPIClient      = "google-cloud-sdk vscode_cloudshelleditor/0.1"
	antigravityClientMetadata = `{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}`
)

// FetchAntigravityProjectID exposes project discovery for external callers.
func FetchAntigravityProjectID(ctx context.Context, accessToken string, httpClient *http.Client) (string, error) {
	return fetchAntigravityProjectID(ctx, accessToken, httpClient)
}

// fetchAntigravityProjectID retrieves the project ID for the authenticated user via loadCodeAssist.
// This uses the same approach as Gemini CLI to get the cloudaicompanionProject.
func fetchAntigravityProjectID(ctx context.Context, accessToken string, httpClient *http.Client) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Call loadCodeAssist to get the project
	loadReqBody := map[string]any{
		"metadata": map[string]string{
			"ideType":    "IDE_UNSPECIFIED",
			"platform":   "PLATFORM_UNSPECIFIED",
			"pluginType": "GEMINI",
		},
	}

	rawBody, errMarshal := json.Marshal(loadReqBody)
	if errMarshal != nil {
		return "", fmt.Errorf("marshal request body: %w", errMarshal)
	}

	endpointURL := fmt.Sprintf("%s/%s:loadCodeAssist", antigravityAPIEndpoint, antigravityAPIVersion)
	status, _, bodyBytes, err := oauthhttp.Do(
		ctx,
		httpClient,
		func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, strings.NewReader(string(rawBody)))
			if err != nil {
				return nil, fmt.Errorf("create request: %w", err)
			}
			req.Header.Set("Authorization", "Bearer "+accessToken)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json")
			req.Header.Set("User-Agent", antigravityAPIUserAgent)
			req.Header.Set("X-Goog-Api-Client", antigravityAPIClient)
			req.Header.Set("Client-Metadata", antigravityClientMetadata)
			return req, nil
		},
		oauthhttp.DefaultRetryConfig(),
	)
	if err != nil && status == 0 {
		return "", fmt.Errorf("execute request: %w", err)
	}

	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		msg := strings.TrimSpace(string(bodyBytes))
		if err != nil {
			return "", fmt.Errorf("request failed with status %d: %s: %w", status, msg, err)
		}
		return "", fmt.Errorf("request failed with status %d: %s", status, msg)
	}
	if err != nil {
		return "", fmt.Errorf("execute request: %w", err)
	}

	var loadResp map[string]any
	if errDecode := json.Unmarshal(bodyBytes, &loadResp); errDecode != nil {
		return "", fmt.Errorf("decode response: %w", errDecode)
	}

	// Extract projectID from response
	projectID := ""
	if id, ok := loadResp["cloudaicompanionProject"].(string); ok {
		projectID = strings.TrimSpace(id)
	}
	if projectID == "" {
		if projectMap, ok := loadResp["cloudaicompanionProject"].(map[string]any); ok {
			if id, okID := projectMap["id"].(string); okID {
				projectID = strings.TrimSpace(id)
			}
		}
	}

	if projectID == "" {
		return "", fmt.Errorf("no cloudaicompanionProject in response")
	}

	return projectID, nil
}
