package zai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultAuthorizeURL     = "https://chat.z.ai/api/oauth/authorize"
	DefaultTokenURL         = "https://zcode.z.ai/api/v1/oauth/token"
	DefaultBusinessLoginURL = "https://api.z.ai/api/auth/z/login"
	DefaultUserInfoURL      = "https://chat.z.ai/api/oauth/userinfo"
	DefaultBizBaseURL       = "https://api.z.ai/api/biz"
	DefaultAppID            = "client_P8X5CMWmlaRO9gyO-KSqtg"
	DefaultBaseURL          = "https://api.z.ai/api/v1"
	// DefaultRedirectURI is the only redirect URI registered for the ZCode
	// OAuth client; Z.AI rejects every loopback URI for it.
	DefaultRedirectURI = "zcode://zai-auth/callback"
)

type TokenBundle struct {
	ZCodeToken    string
	OAuthToken    string
	BusinessToken string
	// InferenceKey / InferenceBaseURL hold the credential used for model
	// requests: the minted coding-plan API key when available, otherwise the
	// business token.
	InferenceKey     string
	InferenceBaseURL string
	ExpiresIn        int
	User             UserInfo
	// PlanKind is "start-plan" when login resolved a Start Plan (free tier)
	// credential; empty means the coding-plan mint path.
	PlanKind string
	// Models is the Start Plan model whitelist captured at login; nil when
	// the account uses the full coding-plan catalog.
	Models []string
	// DeviceMid is the stable per-account device id sent as X-Device-Mid to
	// the zcode-plan endpoints; empty outside the Start Plan path.
	DeviceMid string
}

type UserInfo struct {
	ID     string
	Name   string
	Email  string
	Avatar string
}

type Service struct {
	client           *http.Client
	authorizeURL     string
	tokenURL         string
	businessLoginURL string
	userinfoURL      string
	appID            string
	cliBaseURL       string
	bizBaseURL       string
	startPlanBaseURL string
}

func NewService(client *http.Client) *Service {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Service{
		client:           client,
		authorizeURL:     DefaultAuthorizeURL,
		tokenURL:         DefaultTokenURL,
		businessLoginURL: DefaultBusinessLoginURL,
		userinfoURL:      DefaultUserInfoURL,
		appID:            DefaultAppID,
		cliBaseURL:       DefaultOAuthBaseURL,
		bizBaseURL:       DefaultBizBaseURL,
		startPlanBaseURL: StartPlanBaseURL,
	}
}

func (s *Service) SetEndpoints(authorizeURL, tokenURL, businessLoginURL, userinfoURL string) {
	if s == nil {
		return
	}
	if strings.TrimSpace(authorizeURL) != "" {
		s.authorizeURL = strings.TrimRight(strings.TrimSpace(authorizeURL), "/")
	}
	if strings.TrimSpace(tokenURL) != "" {
		s.tokenURL = strings.TrimRight(strings.TrimSpace(tokenURL), "/")
	}
	if strings.TrimSpace(businessLoginURL) != "" {
		s.businessLoginURL = strings.TrimRight(strings.TrimSpace(businessLoginURL), "/")
	}
	if strings.TrimSpace(userinfoURL) != "" {
		s.userinfoURL = strings.TrimRight(strings.TrimSpace(userinfoURL), "/")
	}
}

func (s *Service) BuildAuthorizeURL(state, redirectURI string) string {
	if s == nil {
		return ""
	}
	if strings.TrimSpace(redirectURI) == "" {
		redirectURI = DefaultRedirectURI
	}
	query := url.Values{}
	query.Set("redirect_uri", redirectURI)
	query.Set("response_type", "code")
	query.Set("client_id", s.appID)
	query.Set("state", strings.TrimSpace(state))
	return s.authorizeURL + "?" + query.Encode()
}

func (s *Service) ExchangeCode(ctx context.Context, code, redirectURI, state string) (*TokenBundle, error) {
	if s == nil || s.client == nil {
		return nil, fmt.Errorf("zai oauth service unavailable")
	}
	body, errMarshal := json.Marshal(map[string]string{
		"provider":     "zai",
		"code":         strings.TrimSpace(code),
		"redirect_uri": strings.TrimSpace(redirectURI),
		"state":        strings.TrimSpace(state),
	})
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal Z.ai token request: %w", errMarshal)
	}
	var response struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Token     string         `json:"token"`
			ExpiresIn int            `json:"expires_in"`
			User      map[string]any `json:"user"`
			ZAI       struct {
				AccessToken    string `json:"access_token"`
				AccessTokenAlt string `json:"accessToken"`
			} `json:"zai"`
		} `json:"data"`
	}
	if errDecode := s.postJSON(ctx, s.tokenURL, body, &response); errDecode != nil {
		return nil, errDecode
	}
	if response.Code != 0 {
		if strings.TrimSpace(response.Msg) != "" {
			return nil, fmt.Errorf("Z.ai token exchange rejected: %s", strings.TrimSpace(response.Msg))
		}
		return nil, fmt.Errorf("Z.ai token exchange rejected with code %d", response.Code)
	}
	oauthToken := strings.TrimSpace(response.Data.ZAI.AccessToken)
	if oauthToken == "" {
		oauthToken = strings.TrimSpace(response.Data.ZAI.AccessTokenAlt)
	}
	if oauthToken == "" {
		return nil, fmt.Errorf("Z.ai token exchange response is missing access_token")
	}
	businessToken, expiresIn, errBusiness := s.ExchangeBusinessToken(ctx, oauthToken)
	if errBusiness != nil {
		return nil, errBusiness
	}
	bundle := &TokenBundle{
		ZCodeToken:    strings.TrimSpace(response.Data.Token),
		OAuthToken:    oauthToken,
		BusinessToken: businessToken,
		ExpiresIn:     expiresIn,
		User:          userInfoFromMap(response.Data.User),
	}
	return bundle, nil
}

func (s *Service) ExchangeBusinessToken(ctx context.Context, oauthToken string) (string, int, error) {
	body, errMarshal := json.Marshal(map[string]string{"token": strings.TrimSpace(oauthToken)})
	if errMarshal != nil {
		return "", 0, fmt.Errorf("marshal Z.ai business token request: %w", errMarshal)
	}
	var data struct {
		AccessToken    string `json:"access_token"`
		AccessTokenAlt string `json:"accessToken"`
		Token          string `json:"token"`
		ExpiresIn      int    `json:"expires_in"`
	}
	if errDecode := s.doEnvelope(ctx, http.MethodPost, s.businessLoginURL, "", body, &data); errDecode != nil {
		return "", 0, errDecode
	}
	token := strings.TrimSpace(data.AccessToken)
	if token == "" {
		token = strings.TrimSpace(data.AccessTokenAlt)
	}
	if token == "" {
		token = strings.TrimSpace(data.Token)
	}
	if token == "" {
		return "", 0, fmt.Errorf("Z.ai business token response is missing access_token")
	}
	return token, data.ExpiresIn, nil
}

func (s *Service) FetchUserInfo(ctx context.Context, oauthToken string) (UserInfo, error) {
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, s.userinfoURL, nil)
	if errReq != nil {
		return UserInfo{}, errReq
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(oauthToken))
	req.Header.Set("Accept", "application/json")
	resp, errDo := s.client.Do(req)
	if errDo != nil {
		return UserInfo{}, fmt.Errorf("Z.ai user info request failed: %w", errDo)
	}
	defer resp.Body.Close()
	data, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return UserInfo{}, fmt.Errorf("read Z.ai user info response: %w", errRead)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return UserInfo{}, fmt.Errorf("Z.ai user info request rejected with status %d", resp.StatusCode)
	}
	var raw map[string]any
	if errJSON := json.Unmarshal(data, &raw); errJSON != nil {
		return UserInfo{}, fmt.Errorf("decode Z.ai user info response: %w", errJSON)
	}
	if nested, ok := raw["data"].(map[string]any); ok {
		raw = nested
	}
	return userInfoFromMap(raw), nil
}

func (s *Service) postJSON(ctx context.Context, endpoint string, body []byte, target any) error {
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if errReq != nil {
		return errReq
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, errDo := s.client.Do(req)
	if errDo != nil {
		return fmt.Errorf("Z.ai OAuth request failed: %w", errDo)
	}
	defer resp.Body.Close()
	data, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return fmt.Errorf("read Z.ai OAuth response: %w", errRead)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Z.ai OAuth request rejected with status %d", resp.StatusCode)
	}
	if errJSON := json.Unmarshal(data, target); errJSON != nil {
		return fmt.Errorf("decode Z.ai OAuth response: %w", errJSON)
	}
	return nil
}

func userInfoFromMap(raw map[string]any) UserInfo {
	if raw == nil {
		return UserInfo{}
	}
	return UserInfo{
		ID:     firstString(raw, "user_id", "id", "sub"),
		Name:   firstString(raw, "name", "username", "preferred_username"),
		Email:  firstString(raw, "email"),
		Avatar: firstString(raw, "avatar", "picture"),
	}
}

func firstString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := raw[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
