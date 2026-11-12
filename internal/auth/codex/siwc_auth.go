package codex

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
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

// GenerateSIWCReauthURL reuses the issued registration when replacing an unusable grant.
func (o *CodexAuth) GenerateSIWCReauthURL(clientID, state, nonce, deviceID, redirectURI string, pkceCodes *PKCECodes) (string, error) {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" || clientID == SIWCDynamicClientID {
		return "", fmt.Errorf("SIWC reauthorization requires the saved issued client ID")
	}
	rawURL, errGenerate := o.GenerateSIWCAuthURL(state, nonce, deviceID, redirectURI, pkceCodes)
	if errGenerate != nil {
		return "", errGenerate
	}
	parsed, errParse := url.Parse(rawURL)
	if errParse != nil {
		return "", fmt.Errorf("failed to parse SIWC authorization URL: %w", errParse)
	}
	query := parsed.Query()
	query.Set("client_id", clientID)
	query.Del("agent_name_hint")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (o *CodexAuth) requestSIWCToken(ctx context.Context, form url.Values, clientID, deviceID string, requireIDToken bool) (result *CodexSIWCTokenData, errResult error) {
	stage := "request-create"
	defer func() {
		if form.Get("grant_type") == "refresh_token" && errResult != nil && !isNonRetryableRefreshErr(errResult) && !IsRefreshPreSendError(errResult) && !IsRefreshOutcomeUnknown(errResult) {
			errResult = &RefreshOutcomeUnknownError{Stage: stage, Cause: errResult}
		}
	}()
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, siwcTokenEndpointURL, strings.NewReader(form.Encode()))
	if errReq != nil {
		errCreate := fmt.Errorf("failed to create SIWC token request: %w", errReq)
		if form.Get("grant_type") == "refresh_token" {
			return nil, &RefreshPreSendError{Stage: "request-create", Cause: errCreate}
		}
		return nil, errCreate
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	stage = "transport"
	request := o.httpClient.Do
	if form.Get("grant_type") == "refresh_token" {
		request = o.doRefreshRequest
	}
	resp, errDo := request(req)
	if errDo != nil {
		return nil, fmt.Errorf("SIWC token request failed: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("SIWC token response body close error: %v", errClose)
		}
	}()

	stage = "response-read"
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return nil, fmt.Errorf("failed to read SIWC token response: %w", errRead)
	}
	if resp.StatusCode != http.StatusOK {
		stage = "response-status"
		if requestID := strings.TrimSpace(resp.Header.Get("x-request-id")); requestID != "" {
			return nil, fmt.Errorf("OpenAI OAuth token request failed (%d, request_id=%q): %s", resp.StatusCode, requestID, strings.TrimSpace(string(body)))
		}
		return nil, fmt.Errorf("OpenAI OAuth token request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	stage = "response-decode"
	tokenResp := ojson.ParseBytes(body)
	if !ojson.ValidBytes(body) || !tokenResp.IsObject() {
		return nil, fmt.Errorf("failed to parse SIWC token response: expected a JSON object")
	}
	accessToken := tokenResp.Get("access_token")
	if accessToken.Type != ojson.String || strings.TrimSpace(accessToken.Str) == "" {
		return nil, fmt.Errorf("OpenAI OAuth token response has invalid access_token")
	}
	refreshToken := tokenResp.Get("refresh_token")
	if refreshToken.Type != ojson.String || strings.TrimSpace(refreshToken.Str) == "" {
		return nil, fmt.Errorf("OpenAI OAuth token response has invalid refresh_token")
	}
	idToken := tokenResp.Get("id_token")
	if idToken.Type != ojson.Null && idToken.Type != ojson.String {
		return nil, fmt.Errorf("OpenAI OAuth token response has invalid id_token")
	}
	if requireIDToken && strings.TrimSpace(idToken.Str) == "" {
		return nil, fmt.Errorf("OpenAI OAuth token response did not contain an ID token")
	}
	scope := tokenResp.Get("scope")
	// An omitted, unchanged scope on refresh must not discard a rotated refresh token.
	if (requireIDToken || scope.Exists()) && (scope.Type != ojson.String || strings.TrimSpace(scope.Str) == "") {
		return nil, fmt.Errorf("OpenAI OAuth token response has invalid scope")
	}
	expiresInResult := tokenResp.Get("expires_in")
	maxExpiresInSeconds := int64(^uint64(0)>>1) / int64(time.Second)
	var expiresIn int64
	switch expiresInResult.Type {
	case ojson.Number:
		f, errParse := strconv.ParseFloat(expiresInResult.Raw, 64)
		if errParse != nil || f <= 0 || math.IsInf(f, 0) || math.IsNaN(f) || f != math.Trunc(f) || f > float64(maxExpiresInSeconds) {
			return nil, fmt.Errorf("OpenAI OAuth token response has invalid expires_in")
		}
		expiresIn = int64(f)
	case ojson.String:
		raw := strings.TrimSpace(expiresInResult.Str)
		if raw == "" {
			return nil, fmt.Errorf("OpenAI OAuth token response has invalid expires_in")
		}
		i, errParse := strconv.ParseInt(raw, 10, 64)
		if errParse == nil {
			if i <= 0 || i > maxExpiresInSeconds {
				return nil, fmt.Errorf("OpenAI OAuth token response has invalid expires_in")
			}
			expiresIn = i
			break
		}
		f, errParse := strconv.ParseFloat(raw, 64)
		if errParse != nil || f <= 0 || math.IsInf(f, 0) || math.IsNaN(f) || f != math.Trunc(f) || f > float64(maxExpiresInSeconds) {
			return nil, fmt.Errorf("OpenAI OAuth token response has invalid expires_in")
		}
		expiresIn = int64(f)
	default:
		return nil, fmt.Errorf("OpenAI OAuth token response has invalid expires_in")
	}

	scopes := strings.Fields(scope.Str)
	if scope.Exists() && !slices.Contains(scopes, SIWCDirectTokenScope) {
		return nil, fmt.Errorf("OpenAI OAuth grant did not include %s", SIWCDirectTokenScope)
	}

	now := time.Now()
	rawTTL := time.Duration(expiresIn) * time.Second
	ttl := rawTTL - SIWCExpiryMargin
	if ttl <= 0 {
		ttl = rawTTL
	}

	return &CodexSIWCTokenData{
		AccessToken:       accessToken.Str,
		RefreshToken:      refreshToken.Str,
		IDToken:           idToken.Str,
		ClientID:          strings.TrimSpace(clientID),
		Scope:             strings.TrimSpace(scope.Str),
		Scopes:            scopes,
		Expire:            now.Add(ttl).Format(time.RFC3339),
		ExpiresAt:         now.Add(rawTTL).Format(time.RFC3339),
		EarliestRefreshAt: tokenResp.Get("earliest_refresh_at").Value(),
		LastRefresh:       now.Format(time.RFC3339),
		DeviceID:          strings.TrimSpace(deviceID),
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

// SIWCRefreshError marks an error as originating from a Sign in with ChatGPT (SIWC) token refresh.
type SIWCRefreshError struct {
	Err error
}

func (e *SIWCRefreshError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *SIWCRefreshError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// IsSIWCRefreshError reports that this error came from the SIWC refresh partition.
func (e *SIWCRefreshError) IsSIWCRefreshError() bool {
	return true
}

// WrapSIWCRefreshError wraps err in a SIWCRefreshError if non-nil.
func WrapSIWCRefreshError(err error) error {
	if err == nil {
		return nil
	}
	var existing *SIWCRefreshError
	if errors.As(err, &existing) {
		return err
	}
	return &SIWCRefreshError{Err: err}
}

// RefreshSIWCTokens refreshes a Sign in with ChatGPT access token using its refresh token and issued client ID.
func (o *CodexAuth) RefreshSIWCTokens(ctx context.Context, refreshToken, clientID, deviceID string) (*CodexSIWCTokenData, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, WrapSIWCRefreshError(fmt.Errorf("SIWC refresh token is required"))
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return nil, WrapSIWCRefreshError(fmt.Errorf("Stored OpenAI OAuth credential does not contain an issued client ID; reconnect ChatGPT"))
	}
	if ctx == nil {
		ctx = context.Background()
	}

	sfKey := clientID + ":" + refreshToken
	result, errSF, _ := siwcRefreshGroup.Do(sfKey, func() (any, error) {
		refreshCtx, cancelRefresh := context.WithTimeout(context.WithoutCancel(ctx), codexRefreshTimeout)
		defer cancelRefresh()
		if errReadiness := siwcRefreshReadiness(refreshCtx); errReadiness != nil {
			return nil, errReadiness
		}
		form := url.Values{
			"grant_type":    {"refresh_token"},
			"client_id":     {clientID},
			"refresh_token": {refreshToken},
			"resource":      {SIWCResource},
		}
		return journalRefresh(refreshCtx, o.journal(ctx), clientID, refreshToken, func() (*CodexSIWCTokenData, error) {
			return o.requestSIWCToken(refreshCtx, form, clientID, deviceID, false)
		}, decodeSIWCRefreshReceipt)
	})
	if errSF != nil {
		return nil, WrapSIWCRefreshError(errSF)
	}
	tokenData, ok := result.(*CodexSIWCTokenData)
	if !ok || tokenData == nil {
		return nil, WrapSIWCRefreshError(fmt.Errorf("SIWC token refresh failed: invalid single-flight result"))
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
		if errors.Is(errRefresh, context.Canceled) || errors.Is(errRefresh, ErrSIWCRefreshDeferred) {
			return nil, errRefresh
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
