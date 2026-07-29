package qwen

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// qwenOAuthHost is the Qianwen OAuth server endpoint.
	qwenOAuthHost = "https://t.qianwenai.com"
	// qwenDeviceCodeURL is the endpoint for requesting device codes.
	qwenDeviceCodeURL = qwenOAuthHost + "/cli/device/code"
	// qwenDeviceTokenURL is the endpoint for polling device authorization.
	qwenDeviceTokenURL = qwenOAuthHost + "/cli/device/token"

	defaultPollInterval      = 5 * time.Second
	maxPollDuration          = 15 * time.Minute
	refreshThresholdSeconds  = 300
	deviceCodeRequestTimeout = 30 * time.Second
	// qwenUserAgent mimics the official CLI so the auth server accepts requests.
	qwenUserAgent = "qianwen-cli/1.0.0"
)

// QwenAuth handles Qianwen authentication flow.
type QwenAuth struct {
	deviceClient *DeviceFlowClient
	cfg          *config.Config
}

// NewQwenAuth creates a new QwenAuth service instance.
func NewQwenAuth(cfg *config.Config) *QwenAuth {
	return &QwenAuth{
		deviceClient: NewDeviceFlowClient(cfg),
		cfg:          cfg,
	}
}

// StartDeviceFlow initiates the device flow authentication.
func (k *QwenAuth) StartDeviceFlow(ctx context.Context) (*DeviceCodeResponse, error) {
	return k.deviceClient.RequestDeviceCode(ctx)
}

// WaitForAuthorization polls for user authorization and returns the auth bundle.
func (k *QwenAuth) WaitForAuthorization(ctx context.Context, deviceCode *DeviceCodeResponse) (*QwenAuthBundle, error) {
	tokenData, err := k.deviceClient.PollForToken(ctx, deviceCode)
	if err != nil {
		return nil, err
	}
	return &QwenAuthBundle{
		TokenData: tokenData,
		DeviceID:  k.deviceClient.deviceID,
	}, nil
}

// CreateTokenStorage creates a new QwenTokenStorage from an auth bundle.
func (k *QwenAuth) CreateTokenStorage(bundle *QwenAuthBundle) *QwenTokenStorage {
	storage := &QwenTokenStorage{
		AccessToken:  bundle.TokenData.AccessToken,
		RefreshToken: bundle.TokenData.RefreshToken,
		TokenType:    "Bearer",
		DeviceID:     strings.TrimSpace(bundle.DeviceID),
		Email:        bundle.TokenData.Email,
		AliyunID:     bundle.TokenData.AliyunID,
		Expired:      bundle.TokenData.ExpiresAt,
		Type:         "qianwen",
	}
	return storage
}

// DeviceFlowClient handles the OAuth2 device flow for Qianwen.
type DeviceFlowClient struct {
	httpClient *http.Client
	cfg        *config.Config
	deviceID   string
}

// NewDeviceFlowClient creates a new device flow client.
func NewDeviceFlowClient(cfg *config.Config) *DeviceFlowClient {
	client := &http.Client{Timeout: deviceCodeRequestTimeout}
	if cfg != nil {
		client = util.SetProxy(&cfg.SDKConfig, client)
	}
	return &DeviceFlowClient{
		httpClient: client,
		cfg:        cfg,
		deviceID:   getOrCreateDeviceID(),
	}
}

// getOrCreateDeviceID returns an in-memory device ID for the authentication flow.
func getOrCreateDeviceID() string {
	return uuid.New().String()
}

// deviceCodeEnvelope is the success envelope returned by the device code endpoint.
type deviceCodeEnvelope struct {
	Success bool `json:"Success"`
	Data    struct {
		Token           string `json:"Token"`
		VerificationUrl string `json:"VerificationUrl"`
		ExpiresIn       int    `json:"ExpiresIn"`
		Interval        int    `json:"Interval"`
	} `json:"Data"`
}

// deviceTokenEnvelope is the success envelope returned by the device token endpoint.
type deviceTokenEnvelope struct {
	Success bool `json:"Success"`
	Data    struct {
		Status      string `json:"Status"`
		Credentials struct {
			AccessToken  string `json:"AccessToken"`
			RefreshToken string `json:"RefreshToken"`
			ExpireTime   string `json:"ExpireTime"`
			User         struct {
				Email        string `json:"Email"`
				AliyunId     string `json:"AliyunId"`
				Organization string `json:"Organization"`
			} `json:"User"`
		} `json:"Credentials"`
	} `json:"Data"`
}

// RequestDeviceCode initiates the device flow by requesting a device code.
// Qianwen requires PKCE, so a code verifier/challenge pair is generated and the
// verifier is retained on the returned response for use during polling.
func (c *DeviceFlowClient) RequestDeviceCode(ctx context.Context) (*DeviceCodeResponse, error) {
	verifier, challenge, errGen := generatePKCE()
	if errGen != nil {
		return nil, fmt.Errorf("qwen: failed to generate PKCE pair: %w", errGen)
	}
	reqURL := fmt.Sprintf("%s?client_id=%s&code_challenge=%s&code_challenge_method=S256",
		qwenDeviceCodeURL, url.QueryEscape(c.deviceID), url.QueryEscape(challenge))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("qwen: failed to create device code request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", qwenUserAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("qwen: device code request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("qwen device code: close body error: %v", errClose)
		}
	}()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("qwen: failed to read device code response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("qwen: device code request failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var envelope deviceCodeEnvelope
	if err = json.Unmarshal(bodyBytes, &envelope); err != nil {
		return nil, fmt.Errorf("qwen: failed to parse device code response: %w", err)
	}
	if !envelope.Success || envelope.Data.Token == "" || envelope.Data.VerificationUrl == "" {
		return nil, fmt.Errorf("qwen: device code response missing token or verification url")
	}

	return &DeviceCodeResponse{
		Token:           envelope.Data.Token,
		VerificationURL: envelope.Data.VerificationUrl,
		ExpiresIn:       envelope.Data.ExpiresIn,
		Interval:        envelope.Data.Interval,
		CodeVerifier:    verifier,
	}, nil
}

// generatePKCE produces a code verifier and its S256 code challenge.
func generatePKCE() (verifier, challenge string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

// PollForToken polls the token endpoint until the user authorizes or the device code expires.
func (c *DeviceFlowClient) PollForToken(ctx context.Context, deviceCode *DeviceCodeResponse) (*QwenTokenData, error) {
	if deviceCode == nil {
		return nil, fmt.Errorf("qwen: device code is nil")
	}

	interval := time.Duration(deviceCode.Interval) * time.Second
	if interval < defaultPollInterval {
		interval = defaultPollInterval
	}

	deadline := time.Now().Add(maxPollDuration)
	if deviceCode.ExpiresIn > 0 {
		codeDeadline := time.Now().Add(time.Duration(deviceCode.ExpiresIn) * time.Second)
		if codeDeadline.Before(deadline) {
			deadline = codeDeadline
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("qwen: context cancelled: %w", ctx.Err())
		case <-ticker.C:
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("qwen: device code expired")
			}
			token, pollErr, shouldContinue := c.pollDeviceToken(ctx, deviceCode.Token, deviceCode.CodeVerifier)
			if token != nil {
				return token, nil
			}
			if !shouldContinue {
				return nil, pollErr
			}
		}
	}
}

// pollDeviceToken attempts a single poll of the device token endpoint.
// Returns (token, error, shouldContinue).
func (c *DeviceFlowClient) pollDeviceToken(ctx context.Context, deviceToken, codeVerifier string) (*QwenTokenData, error, bool) {
	reqURL := fmt.Sprintf("%s?client_id=%s&token=%s",
		qwenDeviceTokenURL, url.QueryEscape(c.deviceID), url.QueryEscape(deviceToken))
	if strings.TrimSpace(codeVerifier) != "" {
		reqURL += "&code_verifier=" + url.QueryEscape(codeVerifier)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("qwen: failed to create token request: %w", err), false
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", qwenUserAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("qwen: token request failed: %w", err), false
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("qwen device token: close body error: %v", errClose)
		}
	}()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("qwen: failed to read token response: %w", err), false
	}

	var envelope deviceTokenEnvelope
	if err = json.Unmarshal(bodyBytes, &envelope); err != nil {
		return nil, fmt.Errorf("qwen: failed to parse token response: %w", err), false
	}

	status := strings.ToLower(strings.TrimSpace(envelope.Data.Status))
	switch status {
	case "complete":
		creds := envelope.Data.Credentials
		if creds.AccessToken == "" {
			return nil, fmt.Errorf("qwen: empty access token in response"), false
		}
		aliyunID := creds.User.AliyunId
		if aliyunID == "" {
			aliyunID = creds.User.Organization
		}
		return &QwenTokenData{
			AccessToken:  creds.AccessToken,
			RefreshToken: creds.RefreshToken,
			ExpiresAt:    normalizeExpireTime(creds.ExpireTime),
			Email:        creds.User.Email,
			AliyunID:     aliyunID,
		}, nil, false
	case "expired_token":
		return nil, fmt.Errorf("qwen: device code expired"), false
	case "access_denied":
		return nil, fmt.Errorf("qwen: access denied by user"), false
	case "authorization_pending", "slow_down", "":
		return nil, nil, true // Continue polling
	default:
		return nil, nil, true // Unknown status, keep polling
	}
}

// normalizeExpireTime converts Qianwen's ExpireTime to RFC3339 when possible.
func normalizeExpireTime(expireTime string) string {
	trimmed := strings.TrimSpace(expireTime)
	if trimmed == "" {
		return ""
	}
	if _, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return trimmed
	}
	// Qianwen may return a numeric unix timestamp (seconds or milliseconds).
	if unix, errParse := parseUnixTimestamp(trimmed); errParse == nil {
		return unix.UTC().Format(time.RFC3339)
	}
	return trimmed
}

func parseUnixTimestamp(value string) (time.Time, error) {
	var n int64
	if _, err := fmt.Sscanf(value, "%d", &n); err != nil {
		return time.Time{}, err
	}
	if n <= 0 {
		return time.Time{}, fmt.Errorf("non-positive timestamp")
	}
	// Heuristic: values above 1e12 are milliseconds.
	if n > 1_000_000_000_000 {
		return time.UnixMilli(n), nil
	}
	return time.Unix(n, 0), nil
}
