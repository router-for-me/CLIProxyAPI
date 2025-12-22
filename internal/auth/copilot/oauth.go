package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/oauthflow"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/oauthhttp"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

const (
	// copilotClientID is GitHub's Copilot CLI OAuth client ID.
	copilotClientID = "Iv1.b507a08c87ecfe98"
	// copilotDeviceCodeURL is the endpoint for requesting device codes.
	copilotDeviceCodeURL = "https://github.com/login/device/code"
	// copilotTokenURL is the endpoint for exchanging device codes for tokens.
	copilotTokenURL = "https://github.com/login/oauth/access_token"
	// copilotUserInfoURL is the endpoint for fetching GitHub user information.
	copilotUserInfoURL = "https://api.github.com/user"
	// defaultPollInterval is the default interval for polling token endpoint.
	defaultPollInterval = 5 * time.Second
	// maxPollDuration is the maximum time to wait for user authorization.
	maxPollDuration = 15 * time.Minute
)

// DeviceFlowClient handles the OAuth2 device flow for GitHub Copilot.
type DeviceFlowClient struct {
	httpClient *http.Client
	cfg        *config.Config
}

// NewDeviceFlowClient creates a new device flow client.
func NewDeviceFlowClient(cfg *config.Config) *DeviceFlowClient {
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil {
		client = util.SetOAuthProxy(&cfg.SDKConfig, client)
	}
	return &DeviceFlowClient{
		httpClient: client,
		cfg:        cfg,
	}
}

// RequestDeviceCode initiates the device flow by requesting a device code from GitHub.
func (c *DeviceFlowClient) RequestDeviceCode(ctx context.Context) (*DeviceCodeResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	data := url.Values{}
	data.Set("client_id", copilotClientID)
	data.Set("scope", "user:email")

	encoded := data.Encode()
	status, _, bodyBytes, err := oauthhttp.Do(
		ctx,
		c.httpClient,
		func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, copilotDeviceCodeURL, strings.NewReader(encoded))
			if err != nil {
				return nil, NewAuthenticationError(ErrDeviceCodeFailed, err)
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Accept", "application/json")
			return req, nil
		},
		oauthhttp.DefaultRetryConfig(),
	)
	if err != nil && status == 0 {
		return nil, NewAuthenticationError(ErrDeviceCodeFailed, err)
	}

	if !isHTTPSuccess(status) {
		msg := strings.TrimSpace(string(bodyBytes))
		if err != nil {
			return nil, NewAuthenticationError(ErrDeviceCodeFailed, fmt.Errorf("status %d: %s: %w", status, msg, err))
		}
		return nil, NewAuthenticationError(ErrDeviceCodeFailed, fmt.Errorf("status %d: %s", status, msg))
	}
	if err != nil {
		return nil, NewAuthenticationError(ErrDeviceCodeFailed, err)
	}

	var deviceCode DeviceCodeResponse
	if err = json.Unmarshal(bodyBytes, &deviceCode); err != nil {
		return nil, NewAuthenticationError(ErrDeviceCodeFailed, err)
	}

	return &deviceCode, nil
}

// PollForToken polls the token endpoint until the user authorizes or the device code expires.
func (c *DeviceFlowClient) PollForToken(ctx context.Context, deviceCode *DeviceCodeResponse) (*CopilotTokenData, error) {
	if deviceCode == nil {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, fmt.Errorf("device code is nil"))
	}
	if ctx == nil {
		ctx = context.Background()
	}

	interval := deviceCode.Interval
	if interval <= 0 || time.Duration(interval)*time.Second < defaultPollInterval {
		interval = int(defaultPollInterval / time.Second)
	}

	device := &oauthflow.DeviceCodeResult{
		DeviceCode:      strings.TrimSpace(deviceCode.DeviceCode),
		UserCode:        strings.TrimSpace(deviceCode.UserCode),
		VerificationURI: strings.TrimSpace(deviceCode.VerificationURI),
		ExpiresIn:       deviceCode.ExpiresIn,
		Interval:        interval,
	}

	token, err := oauthflow.PollDeviceToken(ctx, device, func(pollCtx context.Context) (*oauthflow.TokenResult, error) {
		resp, err := c.exchangeDeviceCode(pollCtx, device.DeviceCode)
		if err == nil {
			meta := map[string]any{}
			if strings.TrimSpace(resp.Scope) != "" {
				meta["scope"] = resp.Scope
			}
			tokenType := strings.TrimSpace(resp.TokenType)
			if tokenType == "" {
				tokenType = "bearer"
			}
			return &oauthflow.TokenResult{
				AccessToken: resp.AccessToken,
				TokenType:   tokenType,
				Metadata:    meta,
			}, nil
		}

		var authErr *AuthenticationError
		if errors.As(err, &authErr) {
			switch authErr.Type {
			case ErrAuthorizationPending.Type:
				return nil, oauthflow.ErrAuthorizationPending
			case ErrSlowDown.Type:
				return nil, oauthflow.ErrSlowDown
			case ErrDeviceCodeExpired.Type:
				return nil, oauthflow.ErrDeviceCodeExpired
			case ErrAccessDenied.Type:
				return nil, oauthflow.ErrAccessDenied
			}
		}
		return nil, err
	})
	if err != nil {
		switch {
		case errors.Is(err, oauthflow.ErrPollingTimeout),
			errors.Is(err, context.DeadlineExceeded),
			errors.Is(err, context.Canceled):
			return nil, NewAuthenticationError(ErrPollingTimeout, err)
		case errors.Is(err, oauthflow.ErrDeviceCodeExpired):
			return nil, ErrDeviceCodeExpired
		case errors.Is(err, oauthflow.ErrAccessDenied):
			return nil, ErrAccessDenied
		}
		return nil, err
	}
	if token == nil {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, fmt.Errorf("token result is nil"))
	}

	scope := ""
	if token.Metadata != nil {
		if raw, ok := token.Metadata["scope"]; ok {
			if val, okStr := raw.(string); okStr {
				scope = strings.TrimSpace(val)
			}
		}
	}
	tokenType := strings.TrimSpace(token.TokenType)
	if tokenType == "" {
		tokenType = "bearer"
	}
	return &CopilotTokenData{
		AccessToken: token.AccessToken,
		TokenType:   tokenType,
		Scope:       scope,
	}, nil
}

// exchangeDeviceCode attempts to exchange the device code for an access token.
func (c *DeviceFlowClient) exchangeDeviceCode(ctx context.Context, deviceCode string) (*CopilotTokenData, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	data := url.Values{}
	data.Set("client_id", copilotClientID)
	data.Set("device_code", deviceCode)
	data.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

	encoded := data.Encode()
	status, _, bodyBytes, err := oauthhttp.Do(
		ctx,
		c.httpClient,
		func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, copilotTokenURL, strings.NewReader(encoded))
			if err != nil {
				return nil, NewAuthenticationError(ErrTokenExchangeFailed, err)
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Accept", "application/json")
			return req, nil
		},
		oauthhttp.DefaultRetryConfig(),
	)
	if err != nil && status == 0 {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, err)
	}

	// GitHub returns 200 for both success and error cases in device flow
	// Check for OAuth error response first
	var oauthResp struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		AccessToken      string `json:"access_token"`
		TokenType        string `json:"token_type"`
		Scope            string `json:"scope"`
	}

	if err = json.Unmarshal(bodyBytes, &oauthResp); err != nil {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, err)
	}

	if oauthResp.Error != "" {
		switch oauthResp.Error {
		case "authorization_pending":
			return nil, ErrAuthorizationPending
		case "slow_down":
			return nil, ErrSlowDown
		case "expired_token":
			return nil, ErrDeviceCodeExpired
		case "access_denied":
			return nil, ErrAccessDenied
		default:
			return nil, NewOAuthError(oauthResp.Error, oauthResp.ErrorDescription, status)
		}
	}

	if oauthResp.AccessToken == "" {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, fmt.Errorf("empty access token"))
	}

	return &CopilotTokenData{
		AccessToken: oauthResp.AccessToken,
		TokenType:   oauthResp.TokenType,
		Scope:       oauthResp.Scope,
	}, nil
}

// FetchUserInfo retrieves the GitHub username for the authenticated user.
func (c *DeviceFlowClient) FetchUserInfo(ctx context.Context, accessToken string) (string, error) {
	if accessToken == "" {
		return "", NewAuthenticationError(ErrUserInfoFailed, fmt.Errorf("access token is empty"))
	}
	if ctx == nil {
		ctx = context.Background()
	}

	status, _, bodyBytes, err := oauthhttp.Do(
		ctx,
		c.httpClient,
		func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, copilotUserInfoURL, nil)
			if err != nil {
				return nil, NewAuthenticationError(ErrUserInfoFailed, err)
			}
			req.Header.Set("Authorization", "Bearer "+accessToken)
			req.Header.Set("Accept", "application/json")
			req.Header.Set("User-Agent", "CLIProxyAPI")
			return req, nil
		},
		oauthhttp.DefaultRetryConfig(),
	)
	if err != nil && status == 0 {
		return "", NewAuthenticationError(ErrUserInfoFailed, err)
	}

	if !isHTTPSuccess(status) {
		msg := strings.TrimSpace(string(bodyBytes))
		if err != nil {
			return "", NewAuthenticationError(ErrUserInfoFailed, fmt.Errorf("status %d: %s: %w", status, msg, err))
		}
		return "", NewAuthenticationError(ErrUserInfoFailed, fmt.Errorf("status %d: %s", status, msg))
	}
	if err != nil {
		return "", NewAuthenticationError(ErrUserInfoFailed, err)
	}

	var userInfo struct {
		Login string `json:"login"`
	}
	if err = json.Unmarshal(bodyBytes, &userInfo); err != nil {
		return "", NewAuthenticationError(ErrUserInfoFailed, err)
	}

	if userInfo.Login == "" {
		return "", NewAuthenticationError(ErrUserInfoFailed, fmt.Errorf("empty username"))
	}

	return userInfo.Login, nil
}
