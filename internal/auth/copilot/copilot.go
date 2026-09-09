// Package copilot implements GitHub device login and Copilot token exchange.
package copilot

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/singleflight"
)

const (
	Provider     = "github-copilot"
	ClientID     = "Iv1.b507a08c87ecfe98"
	APIBaseURL   = "https://api.githubcopilot.com"
	GitHubAPIURL = "https://api.github.com"
	RefreshLead  = time.Minute
)

type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type Token struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	Endpoints struct {
		API string `json:"api"`
	} `json:"endpoints"`
}

func (t Token) BaseURL() string {
	if t.Endpoints.API != "" {
		return strings.TrimRight(t.Endpoints.API, "/")
	}
	return APIBaseURL
}

// HTTPError deliberately omits response bodies, which may contain credentials.
type HTTPError struct{ Code int }

func (e *HTTPError) Error() string {
	return fmt.Sprintf("github-copilot: upstream returned HTTP %d", e.Code)
}
func (e *HTTPError) StatusCode() int { return e.Code }

type Client struct {
	httpClient *http.Client
	githubURL  string
	apiURL     string
	proxyURL   string
}

func NewClient(cfg *config.Config, proxyURL string) *Client {
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
	}
	if strings.TrimSpace(proxyURL) != "" {
		sdkCfg.ProxyURL = proxyURL
	}
	return &Client{httpClient: util.SetProxy(&sdkCfg, &http.Client{}), githubURL: "https://github.com", apiURL: GitHubAPIURL, proxyURL: sdkCfg.ProxyURL}
}

func SetHeaders(h http.Header) {
	h.Set("Accept", "application/json")
	h.Set("User-Agent", "GitHubCopilotChat/0.40.0")
	h.Set("Editor-Version", "vscode/1.104.0")
	h.Set("Editor-Plugin-Version", "copilot-chat/0.40.0")
	h.Set("Copilot-Integration-Id", "vscode-chat")
	h.Set("X-GitHub-Api-Version", "2025-04-01")
}

func (c *Client) request(ctx context.Context, method, endpoint, token string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("github-copilot: create request: %w", err)
	}
	SetHeaders(req.Header)
	if token != "" {
		if !strings.HasPrefix(token, "Bearer ") {
			token = "token " + token
		}
		req.Header.Set("Authorization", token)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, errDo := c.httpClient.Do(req)
	if errDo != nil {
		return fmt.Errorf("github-copilot: request failed: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("github-copilot: close response")
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPError{Code: resp.StatusCode}
	}
	if errDecode := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); errDecode != nil {
		return fmt.Errorf("github-copilot: invalid response: %w", errDecode)
	}
	return nil
}

func (c *Client) StartDeviceFlow(ctx context.Context) (*DeviceCode, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var code DeviceCode
	if err := c.request(ctx, http.MethodPost, c.githubURL+"/login/device/code", "", url.Values{"client_id": {ClientID}, "scope": {"read:user"}}, &code); err != nil {
		return nil, err
	}
	if code.DeviceCode == "" || code.UserCode == "" || code.VerificationURI == "" || code.ExpiresIn <= 0 {
		return nil, fmt.Errorf("github-copilot: incomplete device code response")
	}
	if code.Interval <= 0 {
		code.Interval = 5
	}
	return &code, nil
}

func (c *Client) WaitForAuthorization(ctx context.Context, code *DeviceCode) (string, error) {
	if code == nil || code.ExpiresIn <= 0 {
		return "", fmt.Errorf("github-copilot: invalid device code")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(code.ExpiresIn)*time.Second)
	defer cancel()
	interval := time.Duration(code.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	for {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
		token, oauthError, err := c.pollToken(ctx, code.DeviceCode)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			var networkError net.Error
			var httpError *HTTPError
			if errors.As(err, &networkError) || (errors.As(err, &httpError) && httpError.Code >= 500) {
				interval = min(interval*2, time.Minute)
				continue
			}
			return "", err
		}
		switch oauthError {
		case "":
			return token, nil
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		case "access_denied":
			return "", fmt.Errorf("github-copilot: authorization declined")
		case "expired_token":
			return "", fmt.Errorf("github-copilot: device code expired; start login again")
		default:
			return "", fmt.Errorf("github-copilot: device authorization failed")
		}
	}
}

func (c *Client) pollToken(ctx context.Context, deviceCode string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var result struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	err := c.request(ctx, http.MethodPost, c.githubURL+"/login/oauth/access_token", "", url.Values{"client_id": {ClientID}, "device_code": {deviceCode}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}}, &result)
	if err == nil && result.Error == "" && strings.TrimSpace(result.AccessToken) == "" {
		err = fmt.Errorf("github-copilot: empty GitHub token")
	}
	return result.AccessToken, result.Error, err
}

func (c *Client) Login(ctx context.Context, githubToken string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var user struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
	}
	if err := c.request(ctx, http.MethodGet, c.apiURL+"/user", githubToken, nil, &user); err != nil {
		return nil, err
	}
	if user.ID <= 0 || user.Login == "" {
		return nil, fmt.Errorf("github-copilot: missing GitHub account identity")
	}
	token, err := c.CopilotToken(ctx, githubToken, false)
	if err != nil {
		return nil, err
	}
	return map[string]any{"type": Provider, "auth_kind": "oauth", "access_token": githubToken, "github_login": user.Login, "github_id": user.ID, "email": user.Login, "expired": time.Unix(token.ExpiresAt, 0).UTC().Format(time.RFC3339)}, nil
}

var tokenCache = struct {
	sync.Mutex
	entries map[[32]byte]Token
}{entries: make(map[[32]byte]Token)}
var tokenFlights singleflight.Group

// CopilotToken caches short-lived tokens per account and proxy. Concurrent refreshes
// share one exchange; cancelling a caller does not cancel other callers' credentials.
func (c *Client) CopilotToken(ctx context.Context, githubToken string, force bool) (Token, error) {
	if strings.TrimSpace(githubToken) == "" {
		return Token{}, fmt.Errorf("github-copilot: missing GitHub token; log in again")
	}
	key := sha256.Sum256([]byte(c.apiURL + "\x00" + c.proxyURL + "\x00" + githubToken))
	lookup := func() (Token, bool) {
		tokenCache.Lock()
		defer tokenCache.Unlock()
		token, ok := tokenCache.entries[key]
		return token, ok && time.Now().Add(RefreshLead).Unix() < token.ExpiresAt
	}
	if !force {
		if token, ok := lookup(); ok {
			return token, nil
		}
	}
	result := tokenFlights.DoChan(string(key[:]), func() (any, error) {
		if !force {
			if token, ok := lookup(); ok {
				return token, nil
			}
		}
		exchangeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		var token Token
		if err := c.request(exchangeCtx, http.MethodGet, c.apiURL+"/copilot_internal/v2/token", githubToken, nil, &token); err != nil {
			return Token{}, err
		}
		if token.Token == "" || token.ExpiresAt <= time.Now().Unix() {
			return Token{}, fmt.Errorf("github-copilot: invalid Copilot token response")
		}
		if token.Endpoints.API != "" {
			endpoint, errParse := url.Parse(token.Endpoints.API)
			if errParse != nil || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || !(endpoint.Hostname() == "githubcopilot.com" || strings.HasSuffix(endpoint.Hostname(), ".githubcopilot.com")) {
				return Token{}, fmt.Errorf("github-copilot: invalid API endpoint")
			}
		}
		tokenCache.Lock()
		for k, cached := range tokenCache.entries {
			if cached.ExpiresAt <= time.Now().Unix() {
				delete(tokenCache.entries, k)
			}
		}
		tokenCache.entries[key] = token
		tokenCache.Unlock()
		return token, nil
	})
	select {
	case <-ctx.Done():
		return Token{}, ctx.Err()
	case result := <-result:
		if result.Err != nil {
			return Token{}, result.Err
		}
		return result.Val.(Token), nil
	}
}

// Quota returns GitHub's account snapshot without converting it into token costs.
func (c *Client) Quota(ctx context.Context, githubToken string) (json.RawMessage, error) {
	var result json.RawMessage
	err := c.request(ctx, http.MethodGet, c.apiURL+"/copilot_internal/user", githubToken, nil, &result)
	return result, err
}
