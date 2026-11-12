package codex

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/singleflight"
)

// OAuth configuration constants for OpenAI Sign in with ChatGPT (SIWC).
const (
	SIWCDynamicClientID  = "dynamic_agent_client"
	SIWCAgentNameHint    = "CLIProxyAPI"
	SIWCAuthorizeURL     = "https://auth.openai.com/api/accounts/authorize"
	SIWCTokenURL         = "https://auth.openai.com/api/accounts/oauth/token"
	SIWCResource         = "https://api.openai.com/v1"
	SIWCCallbackHost     = "127.0.0.1"
	SIWCCallbackPort     = 1455
	SIWCCallbackPath     = "/auth/callback"
	SIWCRedirectURI      = "http://127.0.0.1:1455/auth/callback"
	SIWCDirectTokenScope = "chatgpt.tokens.use.direct"
	SIWCScope            = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
	SIWCExpiryMargin     = 3 * time.Minute
	SIWCRefreshLead      = 5 * time.Minute
)

var (
	siwcUUIDPattern      = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	siwcTokenEndpointURL = SIWCTokenURL
	siwcRefreshGroup     singleflight.Group
)

// IsValidUUID reports whether raw is a canonical 36-char UUID string.
func IsValidUUID(raw string) bool {
	return siwcUUIDPattern.MatchString(strings.TrimSpace(raw))
}

// GenerateRandomBase64URL generates a 32-byte cryptographically random base64url string.
func GenerateRandomBase64URL() (string, error) {
	buf := make([]byte, 32)
	if _, errRead := rand.Read(buf); errRead != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", errRead)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// GenerateSIWCAuthURL constructs the OpenAI Sign in with ChatGPT authorization URL.
func (o *CodexAuth) GenerateSIWCAuthURL(state, nonce, deviceID, redirectURI string, pkceCodes *PKCECodes) (string, error) {
	if pkceCodes == nil {
		return "", fmt.Errorf("PKCE codes are required")
	}
	deviceID = strings.TrimSpace(deviceID)
	if !IsValidUUID(deviceID) {
		return "", fmt.Errorf("Sign in with ChatGPT requires a valid device UUID")
	}
	if strings.TrimSpace(state) == "" {
		return "", fmt.Errorf("state is required")
	}
	if strings.TrimSpace(nonce) == "" {
		return "", fmt.Errorf("nonce is required")
	}
	if strings.TrimSpace(redirectURI) == "" {
		redirectURI = SIWCRedirectURI
	}

	agentNameHint := SIWCAgentNameHint
	if o != nil && strings.TrimSpace(o.siwcAgentNameHint) != "" {
		agentNameHint = strings.TrimSpace(o.siwcAgentNameHint)
	}
	params := url.Values{
		"client_id":             {SIWCDynamicClientID},
		"agent_name_hint":       {agentNameHint},
		"ext_agent_host_id":     {"urn:uuid:" + strings.ToLower(deviceID)},
		"response_type":         {"code"},
		"redirect_uri":          {strings.TrimSpace(redirectURI)},
		"resource":              {SIWCResource},
		"scope":                 {SIWCScope},
		"state":                 {strings.TrimSpace(state)},
		"code_challenge":        {pkceCodes.CodeChallenge},
		"code_challenge_method": {"S256"},
		"nonce":                 {strings.TrimSpace(nonce)},
	}
	return fmt.Sprintf("%s?%s", SIWCAuthorizeURL, params.Encode()), nil
}

type siwcTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	Scope        string `json:"scope"`
	ExpiresIn    int    `json:"expires_in"`
}

func (o *CodexAuth) requestSIWCToken(ctx context.Context, form url.Values, clientID, deviceID string, requireIDToken bool) (*CodexSIWCTokenData, error) {
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, siwcTokenEndpointURL, strings.NewReader(form.Encode()))
	if errReq != nil {
		return nil, fmt.Errorf("failed to create SIWC token request: %w", errReq)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, errDo := o.httpClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("SIWC token request failed: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("SIWC token response body close error: %v", errClose)
		}
	}()

	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return nil, fmt.Errorf("failed to read SIWC token response: %w", errRead)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenAI OAuth token request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tokenResp siwcTokenResponse
	if errUnmarshal := json.Unmarshal(body, &tokenResp); errUnmarshal != nil {
		return nil, fmt.Errorf("failed to parse SIWC token response: %w", errUnmarshal)
	}
	if strings.TrimSpace(tokenResp.AccessToken) == "" {
		return nil, fmt.Errorf("OpenAI OAuth token response has invalid access_token")
	}
	if strings.TrimSpace(tokenResp.RefreshToken) == "" {
		return nil, fmt.Errorf("OpenAI OAuth token response has invalid refresh_token")
	}
	if requireIDToken && strings.TrimSpace(tokenResp.IDToken) == "" {
		return nil, fmt.Errorf("OpenAI OAuth token response did not contain an ID token")
	}
	if strings.TrimSpace(tokenResp.Scope) == "" {
		return nil, fmt.Errorf("OpenAI OAuth token response has invalid scope")
	}
	if tokenResp.ExpiresIn <= 0 {
		return nil, fmt.Errorf("OpenAI OAuth token response has invalid expires_in")
	}

	scopes := strings.Fields(tokenResp.Scope)
	if !slices.Contains(scopes, SIWCDirectTokenScope) {
		return nil, fmt.Errorf("OpenAI OAuth grant did not include %s", SIWCDirectTokenScope)
	}

	now := time.Now()
	rawTTL := time.Duration(tokenResp.ExpiresIn) * time.Second
	ttl := rawTTL - SIWCExpiryMargin
	if ttl <= 0 {
		ttl = rawTTL
	}

	return &CodexSIWCTokenData{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		IDToken:      tokenResp.IDToken,
		ClientID:     strings.TrimSpace(clientID),
		Scope:        strings.TrimSpace(tokenResp.Scope),
		Scopes:       scopes,
		Expire:       now.Add(ttl).Format(time.RFC3339),
		ExpiresAt:    now.Add(rawTTL).Format(time.RFC3339),
		LastRefresh:  now.Format(time.RFC3339),
		DeviceID:     strings.TrimSpace(deviceID),
	}, nil
}

// ExchangeSIWCCodeForTokens exchanges a Sign in with ChatGPT authorization code for tokens.
func (o *CodexAuth) ExchangeSIWCCodeForTokens(ctx context.Context, code, issuedClientID, redirectURI, deviceID string, pkceCodes *PKCECodes) (*CodexSIWCTokenData, error) {
	if pkceCodes == nil {
		return nil, fmt.Errorf("PKCE codes are required for SIWC token exchange")
	}
	issuedClientID = strings.TrimSpace(issuedClientID)
	if issuedClientID == "" {
		return nil, fmt.Errorf("OpenAI OAuth registration callback did not contain an issued client ID")
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, fmt.Errorf("authorization code is required")
	}
	if strings.TrimSpace(redirectURI) == "" {
		redirectURI = SIWCRedirectURI
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {issuedClientID},
		"code":          {code},
		"code_verifier": {pkceCodes.CodeVerifier},
		"redirect_uri":  {strings.TrimSpace(redirectURI)},
		"resource":      {SIWCResource},
	}
	return o.requestSIWCToken(ctx, form, issuedClientID, deviceID, true)
}

// RefreshSIWCTokens refreshes a Sign in with ChatGPT access token using its refresh token and issued client ID.
func (o *CodexAuth) RefreshSIWCTokens(ctx context.Context, refreshToken, clientID, deviceID string) (*CodexSIWCTokenData, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, fmt.Errorf("SIWC refresh token is required")
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return nil, fmt.Errorf("Stored OpenAI OAuth credential does not contain an issued client ID; reconnect ChatGPT")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	sfKey := clientID + ":" + refreshToken
	result, errSF, _ := siwcRefreshGroup.Do(sfKey, func() (any, error) {
		refreshCtx, cancelRefresh := context.WithTimeout(context.WithoutCancel(ctx), codexRefreshTimeout)
		defer cancelRefresh()
		form := url.Values{
			"grant_type":    {"refresh_token"},
			"client_id":     {clientID},
			"refresh_token": {refreshToken},
			"resource":      {SIWCResource},
		}
		return o.requestSIWCToken(refreshCtx, form, clientID, deviceID, false)
	})
	if errSF != nil {
		return nil, errSF
	}
	tokenData, ok := result.(*CodexSIWCTokenData)
	if !ok || tokenData == nil {
		return nil, fmt.Errorf("SIWC token refresh failed: invalid single-flight result")
	}
	return tokenData, nil
}

// RefreshSIWCTokensWithRetry refreshes SIWC tokens with exponential backoff.
func (o *CodexAuth) RefreshSIWCTokensWithRetry(ctx context.Context, refreshToken, clientID, deviceID string, maxRetries int) (*CodexSIWCTokenData, error) {
	if maxRetries <= 0 {
		maxRetries = 1
	}
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		tokenData, errRefresh := o.RefreshSIWCTokens(ctx, refreshToken, clientID, deviceID)
		if errRefresh == nil {
			return tokenData, nil
		}
		if isNonRetryableRefreshErr(errRefresh) {
			log.Warnf("SIWC token refresh attempt %d failed with non-retryable error: %v", attempt+1, errRefresh)
			return nil, errRefresh
		}
		lastErr = errRefresh
		log.Warnf("SIWC token refresh attempt %d failed: %v", attempt+1, errRefresh)
	}
	return nil, fmt.Errorf("SIWC token refresh failed after %d attempts: %w", maxRetries, lastErr)
}
