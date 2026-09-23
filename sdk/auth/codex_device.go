package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	codexLoginModeMetadataKey             = "codex_login_mode"
	codexLoginModeDevice                  = "device"
	codexDeviceUserCodeURL                = "https://auth.openai.com/api/accounts/deviceauth/usercode"
	codexDeviceTokenURL                   = "https://auth.openai.com/api/accounts/deviceauth/token"
	codexDeviceVerificationURL            = "https://auth.openai.com/codex/device"
	codexDeviceTokenExchangeRedirectURI   = "https://auth.openai.com/deviceauth/callback"
	codexDeviceTimeout                    = 15 * time.Minute
	codexDeviceDefaultPollIntervalSeconds = 5
)

type codexDeviceUserCodeRequest struct {
	ClientID string `json:"client_id"`
}

type codexDeviceUserCodeResponse struct {
	DeviceAuthID string          `json:"device_auth_id"`
	UserCode     string          `json:"user_code"`
	UserCodeAlt  string          `json:"usercode"`
	Interval     json.RawMessage `json:"interval"`
}

type codexDeviceTokenRequest struct {
	DeviceAuthID string `json:"device_auth_id"`
	UserCode     string `json:"user_code"`
}

type codexDeviceTokenResponse struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
	CodeChallenge     string `json:"code_challenge"`
}

func shouldUseCodexDeviceFlow(opts *LoginOptions) bool {
	if opts == nil || opts.Metadata == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(opts.Metadata[codexLoginModeMetadataKey]), codexLoginModeDevice)
}

func (a *CodexAuthenticator) deviceHTTPClient(cfg *config.Config) *http.Client {
	if a != nil && a.httpClient != nil {
		return a.httpClient
	}
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
	}
	return util.SetProxy(&sdkCfg, &http.Client{})
}

func (a *CodexAuthenticator) effectiveInterval(d time.Duration) time.Duration {
	if a != nil && a.pollIntervalOverride > 0 {
		return a.pollIntervalOverride
	}
	if d <= 0 {
		return time.Duration(codexDeviceDefaultPollIntervalSeconds) * time.Second
	}
	return d
}

func (a *CodexAuthenticator) loginWithDeviceFlow(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	flow, err := a.StartDeviceFlow(ctx, cfg)
	if err != nil {
		return nil, err
	}

	fmt.Println("Starting Codex device authentication...")
	fmt.Printf("Codex device URL: %s\n", flow.VerificationURI)
	fmt.Printf("Codex device code: %s\n", flow.UserCode)

	if !opts.NoBrowser {
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the device URL manually")
		} else if errOpen := browser.OpenURL(flow.VerificationURI); errOpen != nil {
			log.Warnf("Failed to open browser automatically: %v", errOpen)
		}
	}

	return a.CompleteDeviceFlow(ctx, cfg, flow)
}

// StartDeviceFlow requests a Codex device code and returns it without waiting
// for the user to authorize.
func (a *CodexAuthenticator) StartDeviceFlow(ctx context.Context, cfg *config.Config) (*DeviceFlow, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	userCodeResp, err := requestCodexDeviceUserCode(ctx, a.deviceHTTPClient(cfg))
	if err != nil {
		return nil, err
	}

	userCode := strings.TrimSpace(userCodeResp.UserCode)
	if userCode == "" {
		userCode = strings.TrimSpace(userCodeResp.UserCodeAlt)
	}
	deviceAuthID := strings.TrimSpace(userCodeResp.DeviceAuthID)
	if userCode == "" || deviceAuthID == "" {
		return nil, fmt.Errorf("codex device flow did not return required fields")
	}

	return &DeviceFlow{
		Provider:        a.Provider(),
		UserCode:        userCode,
		DeviceCode:      deviceAuthID,
		DeviceAuthID:    deviceAuthID,
		VerificationURI: codexDeviceVerificationURL,
		Interval:        parseCodexDevicePollInterval(userCodeResp.Interval),
	}, nil
}

// PollDeviceFlow exchanges a Codex device code once.
// A pending result means the user has not approved yet. Approval exchanges
// the returned authorization code for a Codex credential.
func (a *CodexAuthenticator) PollDeviceFlow(ctx context.Context, cfg *config.Config, flow *DeviceFlow) (*DeviceFlowPoll, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if flow == nil {
		return nil, fmt.Errorf("codex device flow is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	deviceAuthID := strings.TrimSpace(flow.DeviceAuthID)
	if deviceAuthID == "" {
		deviceAuthID = strings.TrimSpace(flow.DeviceCode)
	}
	userCode := strings.TrimSpace(flow.UserCode)
	if deviceAuthID == "" || userCode == "" {
		return nil, fmt.Errorf("codex device flow is missing device fields")
	}

	tokenResp, pending, err := pollCodexDeviceTokenOnce(ctx, a.deviceHTTPClient(cfg), deviceAuthID, userCode)
	if err != nil {
		return nil, err
	}
	interval := a.effectiveInterval(flow.Interval)
	if pending {
		return &DeviceFlowPoll{Pending: true, Interval: interval}, nil
	}
	record, errExchange := a.exchangeCodexDeviceAuthorization(ctx, cfg, tokenResp)
	if errExchange != nil {
		return nil, errExchange
	}
	return &DeviceFlowPoll{Interval: interval, Auth: record}, nil
}

// CompleteDeviceFlow polls until the Codex device code is authorized or the flow fails.
func (a *CodexAuthenticator) CompleteDeviceFlow(ctx context.Context, cfg *config.Config, flow *DeviceFlow) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if flow == nil {
		return nil, fmt.Errorf("codex device flow is required")
	}
	interval := a.effectiveInterval(flow.Interval)
	deadline := time.Now().Add(codexDeviceTimeout)
	return pollUntilDeviceAuthorized(ctx, interval, deadline, fmt.Errorf("codex device authentication timed out after 15 minutes"), func() (*DeviceFlowPoll, error) {
		polled, err := a.PollDeviceFlow(ctx, cfg, flow)
		if err != nil || polled == nil || polled.Auth != nil {
			return polled, err
		}
		polled.Interval = a.effectiveInterval(polled.Interval)
		flow.Interval = polled.Interval
		return polled, nil
	})
}

func (a *CodexAuthenticator) exchangeCodexDeviceAuthorization(ctx context.Context, cfg *config.Config, tokenResp *codexDeviceTokenResponse) (*coreauth.Auth, error) {
	if tokenResp == nil {
		return nil, fmt.Errorf("codex device flow token response missing required fields")
	}
	authCode := strings.TrimSpace(tokenResp.AuthorizationCode)
	codeVerifier := strings.TrimSpace(tokenResp.CodeVerifier)
	codeChallenge := strings.TrimSpace(tokenResp.CodeChallenge)
	if authCode == "" || codeVerifier == "" || codeChallenge == "" {
		return nil, fmt.Errorf("codex device flow token response missing required fields")
	}

	authSvc := codex.NewCodexAuth(cfg)
	if a != nil && a.httpClient != nil {
		authSvc.SetHTTPClient(a.httpClient)
	}
	authBundle, err := authSvc.ExchangeCodeForTokensWithRedirect(
		ctx,
		authCode,
		codexDeviceTokenExchangeRedirectURI,
		&codex.PKCECodes{
			CodeVerifier:  codeVerifier,
			CodeChallenge: codeChallenge,
		},
	)
	if err != nil {
		return nil, codex.NewAuthenticationError(codex.ErrCodeExchangeFailed, err)
	}
	return a.buildAuthRecord(authSvc, authBundle)
}

func requestCodexDeviceUserCode(ctx context.Context, client *http.Client) (*codexDeviceUserCodeResponse, error) {
	body, err := json.Marshal(codexDeviceUserCodeRequest{ClientID: codex.ClientID})
	if err != nil {
		return nil, fmt.Errorf("failed to encode codex device request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexDeviceUserCodeURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create codex device request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to request codex device code: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read codex device code response: %w", err)
	}

	if !codexDeviceIsSuccessStatus(resp.StatusCode) {
		trimmed := strings.TrimSpace(string(respBody))
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("codex device endpoint is unavailable (status %d)", resp.StatusCode)
		}
		if trimmed == "" {
			trimmed = "empty response body"
		}
		return nil, fmt.Errorf("codex device code request failed with status %d: %s", resp.StatusCode, trimmed)
	}

	var parsed codexDeviceUserCodeResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("failed to decode codex device code response: %w", err)
	}

	return &parsed, nil
}

func pollCodexDeviceTokenOnce(ctx context.Context, client *http.Client, deviceAuthID, userCode string) (*codexDeviceTokenResponse, bool, error) {
	body, err := json.Marshal(codexDeviceTokenRequest{
		DeviceAuthID: deviceAuthID,
		UserCode:     userCode,
	})
	if err != nil {
		return nil, false, fmt.Errorf("failed to encode codex device poll request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexDeviceTokenURL, bytes.NewReader(body))
	if err != nil {
		return nil, false, fmt.Errorf("failed to create codex device poll request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("failed to poll codex device token: %w", err)
	}

	respBody, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		return nil, false, fmt.Errorf("failed to read codex device poll response: %w", readErr)
	}

	switch {
	case codexDeviceIsSuccessStatus(resp.StatusCode):
		var parsed codexDeviceTokenResponse
		if err := json.Unmarshal(respBody, &parsed); err != nil {
			return nil, false, fmt.Errorf("failed to decode codex device token response: %w", err)
		}
		return &parsed, false, nil
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound:
		return nil, true, nil
	default:
		trimmed := strings.TrimSpace(string(respBody))
		if trimmed == "" {
			trimmed = "empty response body"
		}
		return nil, false, fmt.Errorf("codex device token polling failed with status %d: %s", resp.StatusCode, trimmed)
	}
}

func parseCodexDevicePollInterval(raw json.RawMessage) time.Duration {
	defaultInterval := time.Duration(codexDeviceDefaultPollIntervalSeconds) * time.Second
	if len(raw) == 0 {
		return defaultInterval
	}

	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		if seconds, convErr := strconv.Atoi(strings.TrimSpace(asString)); convErr == nil && seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
	}

	var asInt int
	if err := json.Unmarshal(raw, &asInt); err == nil && asInt > 0 {
		return time.Duration(asInt) * time.Second
	}

	return defaultInterval
}

func codexDeviceIsSuccessStatus(code int) bool {
	return code >= 200 && code < 300
}

func (a *CodexAuthenticator) buildAuthRecord(authSvc *codex.CodexAuth, authBundle *codex.CodexAuthBundle) (*coreauth.Auth, error) {
	tokenStorage := authSvc.CreateTokenStorage(authBundle)

	if tokenStorage == nil || tokenStorage.Email == "" {
		return nil, fmt.Errorf("codex token storage missing account information")
	}

	planType := ""
	hashAccountID := ""
	if tokenStorage.IDToken != "" {
		if claims, errParse := codex.ParseJWTToken(tokenStorage.IDToken); errParse == nil && claims != nil {
			planType = strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType)
			accountID := strings.TrimSpace(claims.CodexAuthInfo.ChatgptAccountID)
			if accountID != "" {
				digest := sha256.Sum256([]byte(accountID))
				hashAccountID = hex.EncodeToString(digest[:])[:8]
			}
		}
	}

	fileName := codex.CredentialFileName(tokenStorage.Email, planType, hashAccountID, true)
	metadata := map[string]any{
		"email": tokenStorage.Email,
	}

	fmt.Println("Codex authentication successful")
	if authBundle.APIKey != "" {
		fmt.Println("Codex API key obtained and stored")
	}

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Storage:  tokenStorage,
		Metadata: metadata,
		Attributes: map[string]string{
			"plan_type": planType,
		},
	}, nil
}
