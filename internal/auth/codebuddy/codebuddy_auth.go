package codebuddy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

const (
	BaseURL       = "https://copilot.tencent.com"
	DefaultDomain = "www.codebuddy.cn"
	UserAgent     = "CLI/2.63.2 CodeBuddy/2.63.2"

	codeBuddyStatePath   = "/v2/plugin/auth/state"
	codeBuddyTokenPath   = "/v2/plugin/auth/token"
	codeBuddyRefreshPath = "/v2/plugin/auth/token/refresh"
	pollInterval         = 5 * time.Second
	maxPollDuration      = 5 * time.Minute
	codeLoginPending     = 11217
	codeSuccess          = 0
)

type CodeBuddyAuth struct {
	httpClient *http.Client
	cfg        *config.Config
	baseURL    string
}

func NewCodeBuddyAuth(cfg *config.Config) *CodeBuddyAuth {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil {
		httpClient = util.SetProxy(&cfg.SDKConfig, httpClient)
	}
	return &CodeBuddyAuth{httpClient: httpClient, cfg: cfg, baseURL: BaseURL}
}

// AuthState holds the state and auth URL returned by the auth state API.
type AuthState struct {
	State   string
	AuthURL string
}

// FetchAuthState calls POST /v2/plugin/auth/state?platform=CLI to get the state and login URL.
func (a *CodeBuddyAuth) FetchAuthState(ctx context.Context) (*AuthState, error) {
	stateURL := fmt.Sprintf("%s%s?platform=CLI", a.baseURL, codeBuddyStatePath)
	body := []byte("{}")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, stateURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("codebuddy: failed to create auth state request: %w", err)
	}

	requestID := strings.ReplaceAll(uuid.New().String(), "-", "")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-Domain", "copilot.tencent.com")
	req.Header.Set("X-No-Authorization", "true")
	req.Header.Set("X-No-User-Id", "true")
	req.Header.Set("X-No-Enterprise-Id", "true")
	req.Header.Set("X-No-Department-Info", "true")
	req.Header.Set("X-Product", "SaaS")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("X-Request-ID", requestID)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codebuddy: auth state request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("codebuddy auth state: close body error: %v", errClose)
		}
	}()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("codebuddy: failed to read auth state response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("codebuddy: auth state request returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data *struct {
			State   string `json:"state"`
			AuthURL string `json:"authUrl"`
		} `json:"data"`
	}
	if err = json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("codebuddy: failed to parse auth state response: %w", err)
	}
	if result.Code != codeSuccess {
		return nil, fmt.Errorf("codebuddy: auth state request failed with code %d: %s", result.Code, result.Msg)
	}
	if result.Data == nil || result.Data.State == "" || result.Data.AuthURL == "" {
		return nil, fmt.Errorf("codebuddy: auth state response missing state or authUrl")
	}

	return &AuthState{
		State:   result.Data.State,
		AuthURL: result.Data.AuthURL,
	}, nil
}

type pollResponse struct {
	Code      int    `json:"code"`
	Msg       string `json:"msg"`
	RequestID string `json:"requestId"`
	Data      *struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int64  `json:"expiresIn"`
		TokenType    string `json:"tokenType"`
		Domain       string `json:"domain"`
	} `json:"data"`
}

// doPollRequest 执行单次轮询请求，安全读取并关闭响应体
func (a *CodeBuddyAuth) doPollRequest(ctx context.Context, pollURL string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %v", ErrTokenFetchFailed, err)
	}
	a.applyPollHeaders(req)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("codebuddy poll: close body error: %v", errClose)
		}
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("codebuddy poll: failed to read response body: %w", err)
	}
	return body, resp.StatusCode, nil
}

// PollForToken polls until the user completes browser authorization and returns auth data.
func (a *CodeBuddyAuth) PollForToken(ctx context.Context, state string) (*CodeBuddyTokenStorage, error) {
	deadline := time.Now().Add(maxPollDuration)
	pollURL := fmt.Sprintf("%s%s?state=%s", a.baseURL, codeBuddyTokenPath, url.QueryEscape(state))

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}

		body, statusCode, err := a.doPollRequest(ctx, pollURL)
		if err != nil {
			log.Debugf("codebuddy poll: request error: %v", err)
			continue
		}

		if statusCode != http.StatusOK {
			log.Debugf("codebuddy poll: unexpected status %d", statusCode)
			continue
		}

		var result pollResponse
		if err := json.Unmarshal(body, &result); err != nil {
			continue
		}

		switch result.Code {
		case codeSuccess:
			if result.Data == nil {
				return nil, fmt.Errorf("%w: empty data in response", ErrTokenFetchFailed)
			}
			userID, _ := a.DecodeUserID(result.Data.AccessToken)
			return &CodeBuddyTokenStorage{
				AccessToken:  result.Data.AccessToken,
				RefreshToken: result.Data.RefreshToken,
				ExpiresIn:    result.Data.ExpiresIn,
				TokenType:    result.Data.TokenType,
				Domain:       result.Data.Domain,
				UserID:       userID,
				Type:         "codebuddy",
			}, nil
		case codeLoginPending:
			// continue polling
		default:
			// TODO: when the CodeBuddy API error code for user denial is known,
			// return ErrAccessDenied here instead of ErrTokenFetchFailed.
			return nil, fmt.Errorf("%w: server returned code %d: %s", ErrTokenFetchFailed, result.Code, result.Msg)
		}
	}
	return nil, ErrPollingTimeout
}

// DecodeUserID decodes the sub field from a JWT access token as the user ID.
func (a *CodeBuddyAuth) DecodeUserID(accessToken string) (string, error) {
	parts := strings.Split(accessToken, ".")
	if len(parts) < 2 {
		return "", ErrJWTDecodeFailed
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrJWTDecodeFailed, err)
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("%w: %v", ErrJWTDecodeFailed, err)
	}
	if claims.Sub == "" {
		return "", fmt.Errorf("%w: sub claim is empty", ErrJWTDecodeFailed)
	}
	return claims.Sub, nil
}

// RefreshToken exchanges a refresh token for a new access token.
// It calls POST /v2/plugin/auth/token/refresh with the required headers.
func (a *CodeBuddyAuth) RefreshToken(ctx context.Context, accessToken, refreshToken, userID, domain string) (*CodeBuddyTokenStorage, error) {
	if domain == "" {
		domain = DefaultDomain
	}
	refreshURL := fmt.Sprintf("%s%s", a.baseURL, codeBuddyRefreshPath)
	body := []byte("{}")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, refreshURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("codebuddy: failed to create refresh request: %w", err)
	}

	requestID := strings.ReplaceAll(uuid.New().String(), "-", "")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-Domain", domain)
	req.Header.Set("X-Refresh-Token", refreshToken)
	req.Header.Set("X-Auth-Refresh-Source", "plugin")
	req.Header.Set("X-Request-ID", requestID)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-User-Id", userID)
	req.Header.Set("X-Product", "SaaS")
	req.Header.Set("User-Agent", UserAgent)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codebuddy: refresh request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("codebuddy refresh: close body error: %v", errClose)
		}
	}()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("codebuddy: failed to read refresh response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("codebuddy: refresh token rejected (status %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("codebuddy: refresh failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data *struct {
			AccessToken      string `json:"accessToken"`
			RefreshToken     string `json:"refreshToken"`
			ExpiresIn        int64  `json:"expiresIn"`
			RefreshExpiresIn int64  `json:"refreshExpiresIn"`
			TokenType        string `json:"tokenType"`
			Domain           string `json:"domain"`
		} `json:"data"`
	}
	if err = json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("codebuddy: failed to parse refresh response: %w", err)
	}
	if result.Code != codeSuccess {
		return nil, fmt.Errorf("codebuddy: refresh failed with code %d: %s", result.Code, result.Msg)
	}
	if result.Data == nil {
		return nil, fmt.Errorf("codebuddy: empty data in refresh response")
	}

	newUserID, _ := a.DecodeUserID(result.Data.AccessToken)
	if newUserID == "" {
		newUserID = userID
	}
	tokenDomain := result.Data.Domain
	if tokenDomain == "" {
		tokenDomain = domain
	}

	return &CodeBuddyTokenStorage{
		AccessToken:      result.Data.AccessToken,
		RefreshToken:     result.Data.RefreshToken,
		ExpiresIn:        result.Data.ExpiresIn,
		RefreshExpiresIn: result.Data.RefreshExpiresIn,
		TokenType:        result.Data.TokenType,
		Domain:           tokenDomain,
		UserID:           newUserID,
		Type:             "codebuddy",
	}, nil
}

func (a *CodeBuddyAuth) applyPollHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-No-Authorization", "true")
	req.Header.Set("X-No-User-Id", "true")
	req.Header.Set("X-No-Enterprise-Id", "true")
	req.Header.Set("X-No-Department-Info", "true")
	req.Header.Set("X-Product", "SaaS")
}
