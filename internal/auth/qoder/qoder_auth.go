package qoder

import (
	"context"
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
	// QoderOpenAPIBase is the base URL for Qoder OpenAPI
	QoderOpenAPIBase = "https://openapi.qoder.sh"
	// QoderCenterBase is the base URL for Qoder Center API
	QoderCenterBase = "https://center.qoder.sh"
	// QoderChatBase is the inference host used for chat / model list /
	// other algo-prefixed endpoints. Veria's reverse-engineering put this
	// at api3.qoder.sh; older IDE builds used api1.
	QoderChatBase = "https://api3.qoder.sh"
	// QoderLoginURL is the URL for user authentication
	QoderLoginURL = "https://qoder.com/device/selectAccounts"
	// QoderOAuthTokenEndpoint is the URL for polling device code token
	QoderOAuthTokenEndpoint = "https://openapi.qoder.sh/api/v1/deviceToken/poll"
	// QoderRefreshTokenEndpoint is the URL for refreshing access tokens
	QoderRefreshTokenEndpoint = "https://center.qoder.sh/algo/api/v3/user/refresh_token"
	// QoderUserInfoEndpoint is the URL for fetching user information
	QoderUserInfoEndpoint = "https://openapi.qoder.sh/api/v1/userinfo"
	// QoderIDEVersion is the upstream client version that the COSY signature
	// scheme expects in payload.cosyVersion and the Cosy-Version header.
	// 1.0.0 = what qodercli 0.2.16 actually sends in the COSY payload and
	// Cosy-Version header (captured from live traffic). Earlier builds sent
	// 0.14.2 (IDE) and qoder2api sends 0.1.43 — server accepts any of these
	// as long as headers are consistent. Bump cautiously.
	QoderIDEVersion = "1.0.0"
	// QoderClientType is the client type advertised in the Cosy-Clienttype
	// header. NPM qodercli (0.2.16) sends "5" (CLI). IDE/web sends "0".
	QoderClientType = "5"
	// QoderDataPolicy is the value sent in the Cosy-Data-Policy header.
	// qodercli sends "disagree" (opt-out of training data collection).
	QoderDataPolicy = "disagree"
	// QoderLoginVersion is the value sent in the Login-Version header.
	// "v2" is what current qodercli/IDE builds advertise.
	QoderLoginVersion = "v2"
	// QoderMachineOS is the machine OS identifier sent in COSY headers.
	// qodercli's signing scheme treats this as a fixed magic string; the
	// real client sends "x86_64_windows" regardless of host OS.
	QoderMachineOS = "x86_64_windows"
	// QoderMachineTypeMagic is sent as Cosy-Machinetype.
	// qodercli sends "5" (same as client type).
	QoderMachineTypeMagic = "5"
)

// QoderTokenData represents the OAuth credentials from device flow polling
type QoderTokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpireTime   int64  `json:"expire_time"`
	UserID       string `json:"user_id"`
	MachineToken string `json:"machineToken"`
	MachineType  string `json:"machineType"`
}

// DeviceFlowResponse represents the response from the device authorization endpoint
type DeviceFlowResponse struct {
	// VerificationURIComplete is the full URL with PKCE challenge for user authentication
	VerificationURIComplete string `json:"verification_uri_complete"`
	// CodeVerifier is the PKCE code verifier (generated locally, not from server)
	CodeVerifier string `json:"code_verifier"`
	// Nonce is the random nonce for the request
	Nonce string `json:"nonce"`
	// MachineID is the machine identifier
	MachineID string `json:"machine_id"`
}

// DeviceFlowPollResponse mirrors the actual /api/v1/deviceToken/poll success
// payload, e.g.:
//
//	{
//	  "id": "019e34c9-...",
//	  "token": "dt-xwVyvraeJKzjDfLbM6ANNy9d",
//	  "user_id": "019cbc72-...",
//	  "code_challenge": "...",
//	  "expires_at": "2026-06-16T07:15:04Z",
//	  "refresh_token": "drt-AQHr26ttbx1nAZrKit4g7dns",
//	  "expires_in": 2591999998,
//	  "refresh_token_expires_in": 31103999999,
//	  "refresh_token_expires_at": "2027-05-12T07:15:04Z"
//	}
//
// The fields are flat (no "data" wrapper). expires_at / refresh_token_expires_at
// are RFC3339 strings; expires_in / refresh_token_expires_in are seconds-from-now.
type DeviceFlowPollResponse struct {
	ID                    string `json:"id"`
	Token                 string `json:"token"`
	UserID                string `json:"user_id"`
	RefreshToken          string `json:"refresh_token"`
	RefreshTokenID        string `json:"refresh_token_id"`
	ExpiresAt             string `json:"expires_at"`
	ExpiresIn             int64  `json:"expires_in"`
	RefreshTokenExpiresAt string `json:"refresh_token_expires_at"`
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
	CreatedAt             string `json:"created_at"`
	UpdatedAt             string `json:"updated_at"`
}

// RefreshTokenResponse mirrors /algo/api/v3/user/refresh_token.
// Treated as the same shape as the poll response until proven otherwise; if the
// upstream returns a different schema we'll see it via the empty-token error.
type RefreshTokenResponse = DeviceFlowPollResponse

// UserInfoResponse represents the response from /api/v1/userinfo. The endpoint
// returns a flat JSON object (no "data" wrapper):
//
//	{"id":"019cbc72-...", "name":"...", "username":"...",
//	 "email":"shiminhao964@example.com", "organization_id":"...", ...}
type UserInfoResponse struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Username       string `json:"username"`
	Email          string `json:"email"`
	OrganizationID string `json:"organization_id"`
}

// QoderAuth manages authentication and token handling for the Qoder API
type QoderAuth struct {
	httpClient *http.Client
}

// NewQoderAuth creates a new QoderAuth instance with a proxy-configured HTTP client
func NewQoderAuth(cfg *config.Config) *QoderAuth {
	return &QoderAuth{
		httpClient: util.SetProxy(&cfg.SDKConfig, &http.Client{}),
	}
}

// InitiateDeviceFlow starts the OAuth 2.0 device authorization flow.
// Qoder uses a simplified flow: generate PKCE locally and construct the login URL.
func (qa *QoderAuth) InitiateDeviceFlow(ctx context.Context) (*DeviceFlowResponse, error) {
	codeVerifier, codeChallenge, err := generateDevicePKCEPair()
	if err != nil {
		return nil, fmt.Errorf("failed to generate PKCE pair: %w", err)
	}

	nonce := uuid.New().String()
	machineID := generateMachineID()

	verificationURI := fmt.Sprintf(
		"%s?challenge=%s&challenge_method=S256&machine_id=%s&nonce=%s",
		QoderLoginURL,
		codeChallenge,
		machineID,
		nonce,
	)

	return &DeviceFlowResponse{
		VerificationURIComplete: verificationURI,
		CodeVerifier:            codeVerifier,
		Nonce:                   nonce,
		MachineID:               machineID,
	}, nil
}

// PollForToken polls the token endpoint with the device code to obtain an access token.
func (qa *QoderAuth) PollForToken(ctx context.Context, deviceFlow *DeviceFlowResponse) (*QoderTokenData, error) {
	if deviceFlow == nil || deviceFlow.CodeVerifier == "" || deviceFlow.Nonce == "" {
		return nil, fmt.Errorf("device flow is missing code verifier or nonce")
	}

	pollURL := fmt.Sprintf(
		"%s?nonce=%s&verifier=%s&challenge_method=S256",
		QoderOAuthTokenEndpoint,
		url.QueryEscape(deviceFlow.Nonce),
		url.QueryEscape(deviceFlow.CodeVerifier),
	)

	pollInterval := 2 * time.Second
	maxAttempts := 90 // 3 minutes max (180 seconds / 2 seconds per poll)

	for attempt := 0; attempt < maxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		req, err := http.NewRequestWithContext(ctx, "GET", pollURL, nil)
		if err != nil {
			log.Warnf("Polling attempt %d/%d failed: %v", attempt+1, maxAttempts, err)
			time.Sleep(pollInterval)
			continue
		}

		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "Go-http-client/2.0")

		resp, err := qa.httpClient.Do(req)
		if err != nil {
			log.Warnf("Polling attempt %d/%d failed: %v", attempt+1, maxAttempts, err)
			time.Sleep(pollInterval)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			log.Warnf("Polling attempt %d/%d failed: %v", attempt+1, maxAttempts, err)
			time.Sleep(pollInterval)
			continue
		}

		if resp.StatusCode == http.StatusAccepted {
			// Still pending - continue polling
			log.Debugf("Polling attempt %d/%d... (pending)", attempt+1, maxAttempts)
			time.Sleep(pollInterval)
			continue
		}

		if resp.StatusCode == http.StatusNotFound {
			// Token not created yet - user hasn't authenticated, continue polling
			log.Debugf("Polling attempt %d/%d... (token not found, waiting for auth)", attempt+1, maxAttempts)
			time.Sleep(pollInterval)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			// Parse error response
			var errorData map[string]interface{}
			if err = json.Unmarshal(body, &errorData); err == nil {
				if errMsg, ok := errorData["message"].(string); ok {
					return nil, fmt.Errorf("device token poll failed: %s", errMsg)
				}
			}
			return nil, fmt.Errorf("device token poll failed: %d %s. Response: %s", resp.StatusCode, resp.Status, string(body))
		}

		// Success - parse token data
		var response DeviceFlowPollResponse
		if err = json.Unmarshal(body, &response); err != nil {
			return nil, fmt.Errorf("failed to parse token response: %w", err)
		}

		// Defensive: surface a clear error if the upstream returned 200 but
		// the token field is empty.
		if response.Token == "" {
			return nil, fmt.Errorf("device token poll returned empty access token; raw response keys may have changed")
		}

		expireMs := parseExpiresAt(response.ExpiresAt, response.ExpiresIn)

		return &QoderTokenData{
			AccessToken:  response.Token,
			RefreshToken: response.RefreshToken,
			ExpireTime:   expireMs,
			UserID:       response.UserID,
		}, nil
	}

	return nil, fmt.Errorf("authentication timeout. Please restart the authentication process")
}

// RefreshTokens exchanges a refresh token for a new access token
func (qa *QoderAuth) RefreshTokens(ctx context.Context, accessToken, refreshToken string) (*QoderTokenData, error) {
	reqBody := map[string]string{
		"refreshToken": refreshToken,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal refresh request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", QoderRefreshTokenEndpoint, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, fmt.Errorf("failed to create refresh request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := qa.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errorData map[string]interface{}
		if err = json.Unmarshal(body, &errorData); err == nil {
			if errMsg, ok := errorData["message"].(string); ok {
				return nil, fmt.Errorf("token refresh failed: %s", errMsg)
			}
		}
		return nil, fmt.Errorf("token refresh failed: %d %s. Response: %s", resp.StatusCode, resp.Status, string(body))
	}

	var response RefreshTokenResponse
	if err = json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse refresh response: %w", err)
	}

	if response.Token == "" {
		return nil, fmt.Errorf("token refresh returned empty access token; raw response keys may have changed")
	}

	expireMs := parseExpiresAt(response.ExpiresAt, response.ExpiresIn)

	return &QoderTokenData{
		AccessToken:  response.Token,
		RefreshToken: response.RefreshToken,
		ExpireTime:   expireMs,
	}, nil
}

// FetchUserInfo fetches user information from the API
func (qa *QoderAuth) FetchUserInfo(ctx context.Context, accessToken string) (name, email string, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", QoderUserInfoEndpoint, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to create user info request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Go-http-client/2.0")

	resp, err := qa.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("user info request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("failed to read user info response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("user info request failed: %d %s", resp.StatusCode, resp.Status)
	}

	var response UserInfoResponse
	if err = json.Unmarshal(body, &response); err != nil {
		return "", "", fmt.Errorf("failed to parse user info response: %w", err)
	}

	name = strings.TrimSpace(response.Name)
	if name == "" {
		name = strings.TrimSpace(response.Username)
	}
	email = strings.TrimSpace(response.Email)

	return name, email, nil
}

// SaveUserInfo stores the user info alongside auth metadata for later use.
// This mirrors the behavior in qoder-direct.py where user_id is persisted
// and userinfo fields are updated if available.
func (qa *QoderAuth) SaveUserInfo(ctx context.Context, accessToken, userID, name, email string) (string, string) {
	if strings.TrimSpace(accessToken) == "" {
		return name, email
	}

	if strings.TrimSpace(name) == "" || strings.TrimSpace(email) == "" {
		if fetchedName, fetchedEmail, err := qa.FetchUserInfo(ctx, accessToken); err == nil {
			if strings.TrimSpace(name) == "" {
				name = fetchedName
			}
			if strings.TrimSpace(email) == "" {
				email = fetchedEmail
			}
		}
	}

	return name, email
}

// CreateTokenStorage creates a QoderTokenStorage object from a QoderTokenData object
func (qa *QoderAuth) CreateTokenStorage(tokenData *QoderTokenData, machineID string) *QoderTokenStorage {
	storage := &QoderTokenStorage{
		Token:        tokenData.AccessToken,
		RefreshToken: tokenData.RefreshToken,
		UserID:       tokenData.UserID,
		ExpireTime:   tokenData.ExpireTime,
		LastRefresh:  time.Now().Format(time.RFC3339),
		MachineID:    machineID,
		MachineToken: tokenData.MachineToken,
		MachineType:  tokenData.MachineType,
	}

	return storage
}

// UpdateTokenStorage updates an existing token storage with new token data
func (qa *QoderAuth) UpdateTokenStorage(storage *QoderTokenStorage, tokenData *QoderTokenData) {
	storage.Token = tokenData.AccessToken
	storage.RefreshToken = tokenData.RefreshToken
	storage.ExpireTime = tokenData.ExpireTime
	storage.LastRefresh = time.Now().Format(time.RFC3339)
}

// RefreshTokensWithRetry attempts to refresh tokens with a specified number of retries upon failure
func (qa *QoderAuth) RefreshTokensWithRetry(ctx context.Context, accessToken, refreshToken string, maxRetries int) (*QoderTokenData, error) {
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Wait before retry
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}

		tokenData, err := qa.RefreshTokens(ctx, accessToken, refreshToken)
		if err == nil {
			return tokenData, nil
		}

		lastErr = err
		log.Warnf("Token refresh attempt %d/%d failed: %v", attempt+1, maxRetries, err)
	}

	return nil, fmt.Errorf("token refresh failed after %d attempts: %w", maxRetries, lastErr)
}
