package codebuddy_ai

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

	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

const (
	BaseURL       = "https://www.codebuddy.ai"
	DefaultDomain = "www.codebuddy.ai"
	UserAgent     = "CodeBuddy/1.100.0"

	authStatePath    = "/v2/plugin/auth/state"
	authTokenPath    = "/v2/plugin/auth/token"
	authRefreshPath  = "/v2/plugin/auth/token/refresh"
	pollInterval     = 3 * time.Second
	maxPollDuration  = 5 * time.Minute
	codeLoginPending = 11217
	codeSuccess      = 0
)

type CodeBuddyAIAuth struct {
	httpClient *http.Client
	cfg        *config.Config
	baseURL    string
}

func NewCodeBuddyAIAuth(cfg *config.Config) *CodeBuddyAIAuth {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil {
		httpClient = util.SetProxy(&cfg.SDKConfig, httpClient)
	}
	return &CodeBuddyAIAuth{httpClient: httpClient, cfg: cfg, baseURL: BaseURL}
}

type AuthState struct {
	State   string
	AuthURL string
}

func (a *CodeBuddyAIAuth) FetchAuthState(ctx context.Context) (*AuthState, error) {
	stateURL := fmt.Sprintf("%s%s?platform=ide", a.baseURL, authStatePath)
	body := []byte("{}")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, stateURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("codebuddy-ai: failed to create auth state request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-No-Authorization", "true")
	req.Header.Set("X-No-User-Id", "true")
	req.Header.Set("X-No-Enterprise-Id", "true")
	req.Header.Set("User-Agent", UserAgent)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codebuddy-ai: auth state request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("codebuddy-ai auth state: close body error: %v", errClose)
		}
	}()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("codebuddy-ai: failed to read auth state response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("codebuddy-ai: auth state request returned status %d: %s", resp.StatusCode, string(bodyBytes))
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
		return nil, fmt.Errorf("codebuddy-ai: failed to parse auth state response: %w", err)
	}
	if result.Code != codeSuccess {
		return nil, fmt.Errorf("codebuddy-ai: auth state request failed with code %d: %s", result.Code, result.Msg)
	}
	if result.Data == nil || result.Data.State == "" || result.Data.AuthURL == "" {
		return nil, fmt.Errorf("codebuddy-ai: auth state response missing state or authUrl")
	}

	return &AuthState{
		State:   result.Data.State,
		AuthURL: result.Data.AuthURL,
	}, nil
}

type pollResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data *struct {
		AccessToken      string `json:"accessToken"`
		RefreshToken     string `json:"refreshToken"`
		ExpiresIn        int64  `json:"expiresIn"`
		RefreshExpiresIn int64  `json:"refreshExpiresIn"`
		TokenType        string `json:"tokenType"`
	} `json:"data"`
}

func (a *CodeBuddyAIAuth) PollForToken(ctx context.Context, state string) (*CodeBuddyAITokenStorage, error) {
	deadline := time.Now().Add(maxPollDuration)
	pollURL := fmt.Sprintf("%s%s?state=%s", a.baseURL, authTokenPath, url.QueryEscape(state))

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrTokenFetchFailed, err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-No-Authorization", "true")
		req.Header.Set("User-Agent", UserAgent)

		resp, err := a.httpClient.Do(req)
		if err != nil {
			log.Debugf("codebuddy-ai poll: request error: %v", err)
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Debugf("codebuddy-ai poll: read error: %v", err)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			log.Debugf("codebuddy-ai poll: unexpected status %d", resp.StatusCode)
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
			return &CodeBuddyAITokenStorage{
				AccessToken:      result.Data.AccessToken,
				RefreshToken:     result.Data.RefreshToken,
				ExpiresIn:        result.Data.ExpiresIn,
				RefreshExpiresIn: result.Data.RefreshExpiresIn,
				TokenType:        result.Data.TokenType,
				Domain:           DefaultDomain,
				UserID:           userID,
				Type:             "codebuddy-ai",
			}, nil
		case codeLoginPending:
		default:
			return nil, fmt.Errorf("%w: server returned code %d: %s", ErrTokenFetchFailed, result.Code, result.Msg)
		}
	}
	return nil, ErrPollingTimeout
}

func (a *CodeBuddyAIAuth) DecodeUserID(accessToken string) (string, error) {
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

func (a *CodeBuddyAIAuth) RefreshToken(ctx context.Context, accessToken, refreshToken, userID, domain string) (*CodeBuddyAITokenStorage, error) {
	if domain == "" {
		domain = DefaultDomain
	}
	refreshURL := fmt.Sprintf("%s%s", a.baseURL, authRefreshPath)
	body := []byte("{}")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, refreshURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("codebuddy-ai: failed to create refresh request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Domain", domain)
	req.Header.Set("X-Refresh-Token", refreshToken)
	req.Header.Set("X-Auth-Refresh-Source", "ide-main")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-User-Id", userID)
	req.Header.Set("User-Agent", UserAgent)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codebuddy-ai: refresh request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("codebuddy-ai refresh: close body error: %v", errClose)
		}
	}()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("codebuddy-ai: failed to read refresh response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("codebuddy-ai: refresh token rejected (status %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("codebuddy-ai: refresh failed with status %d: %s", resp.StatusCode, string(bodyBytes))
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
		} `json:"data"`
	}
	if err = json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("codebuddy-ai: failed to parse refresh response: %w", err)
	}
	if result.Code != codeSuccess {
		return nil, fmt.Errorf("codebuddy-ai: refresh failed with code %d: %s", result.Code, result.Msg)
	}
	if result.Data == nil {
		return nil, fmt.Errorf("codebuddy-ai: empty data in refresh response")
	}

	newUserID, _ := a.DecodeUserID(result.Data.AccessToken)
	if newUserID == "" {
		newUserID = userID
	}

	return &CodeBuddyAITokenStorage{
		AccessToken:      result.Data.AccessToken,
		RefreshToken:     result.Data.RefreshToken,
		ExpiresIn:        result.Data.ExpiresIn,
		RefreshExpiresIn: result.Data.RefreshExpiresIn,
		TokenType:        result.Data.TokenType,
		Domain:           domain,
		UserID:           newUserID,
		Type:             "codebuddy-ai",
	}, nil
}
