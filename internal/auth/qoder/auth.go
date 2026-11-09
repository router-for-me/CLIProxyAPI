package qoder

import (
	"bytes"
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
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	deviceLoginTTL       = 5 * time.Minute
	devicePollInterval   = time.Second
	maxAuthResponseBytes = 1 << 20
)

// DeviceFlow contains the private PKCE state needed to complete one login.
type DeviceFlow struct {
	VerificationURI string
	CodeVerifier    string
	Nonce           string
	MachineID       string
}

// Client performs Qoder credential-acquisition requests.
type Client struct {
	region       string
	endpoints    Endpoints
	httpClient   *http.Client
	pollInterval time.Duration
	loginTTL     time.Duration
	now          func() time.Time
}

// NewClient creates a region-pinned OAuth client using the configured proxy.
func NewClient(cfg *config.Config, region string) *Client {
	endpoints, _ := ResolveEndpoints(region)
	var sdkCfg *config.SDKConfig
	if cfg != nil {
		sdkCfg = &cfg.SDKConfig
	}
	return &Client{
		region:       NormalizeRegion(region),
		endpoints:    endpoints,
		httpClient:   util.SetProxy(sdkCfg, &http.Client{Timeout: 30 * time.Second}),
		pollInterval: devicePollInterval,
		loginTTL:     deviceLoginTTL,
		now:          time.Now,
	}
}

// SetHTTPClient overrides the transport, primarily for deterministic tests.
func (c *Client) SetHTTPClient(client *http.Client) {
	if client != nil {
		c.httpClient = client
	}
}

// StartDeviceFlow creates the official PKCE device authorization URL locally.
func (c *Client) StartDeviceFlow() (*DeviceFlow, error) {
	verifier, err := randomURLSafe(64)
	if err != nil {
		return nil, fmt.Errorf("qoder: generate PKCE verifier: %w", err)
	}
	machineID, err := randomURLSafe(72)
	if err != nil {
		return nil, fmt.Errorf("qoder: generate machine id: %w", err)
	}
	challengeBytes := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"challenge":        {base64.RawURLEncoding.EncodeToString(challengeBytes[:])},
		"challenge_method": {"S256"},
		"nonce":            {uuid.NewString()},
		"machine_id":       {machineID},
		"client_id":        {deviceClientID},
	}
	return &DeviceFlow{
		VerificationURI: c.endpoints.LoginURL() + "?" + query.Encode(),
		CodeVerifier:    verifier,
		Nonce:           query.Get("nonce"),
		MachineID:       machineID,
	}, nil
}

// PollForToken waits for browser approval and then enriches the account identity.
func (c *Client) PollForToken(ctx context.Context, flow *DeviceFlow) (*TokenStorage, error) {
	if flow == nil || flow.CodeVerifier == "" || flow.Nonce == "" || flow.MachineID == "" {
		return nil, fmt.Errorf("qoder: incomplete device login state")
	}
	query := url.Values{
		"nonce":            {flow.Nonce},
		"verifier":         {flow.CodeVerifier},
		"challenge_method": {"S256"},
	}
	endpoint := c.endpoints.DevicePollURL() + "?" + query.Encode()
	deadline := c.now().Add(c.loginTTL)
	for {
		if errContext := ctx.Err(); errContext != nil {
			return nil, errContext
		}
		if !c.now().Before(deadline) {
			return nil, fmt.Errorf("qoder: device authorization timed out")
		}
		storage, pending, err := c.pollOnce(ctx, endpoint, flow.MachineID)
		if err != nil {
			return nil, err
		}
		if !pending {
			if errUser := c.populateUserInfo(ctx, storage); errUser != nil {
				return nil, errUser
			}
			return storage, nil
		}
		timer := time.NewTimer(c.pollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *Client) pollOnce(ctx context.Context, endpoint, machineID string) (*TokenStorage, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false, err
	}
	c.applyOpenAPIHeaders(req)
	status, body, err := c.do(req)
	if err != nil {
		return nil, false, fmt.Errorf("qoder: device token poll: %w", err)
	}
	if status == http.StatusNotFound || status == http.StatusAccepted {
		return nil, true, nil
	}
	if status != http.StatusOK {
		return nil, false, fmt.Errorf("qoder: device token poll returned HTTP %d: %s", status, responseSummary(body))
	}
	payload, err := decodeObject(body)
	if err != nil {
		return nil, false, fmt.Errorf("qoder: decode device token: %w", err)
	}
	payload = unwrapPayload(payload)
	token := firstMetadataString(payload, "token", "device_token", "access_token")
	if token == "" {
		return nil, false, fmt.Errorf("qoder: device token response has no token")
	}
	now := c.now()
	storage := &TokenStorage{
		Type:                   ProviderForRegion(c.region),
		Region:                 NormalizeRegion(c.region),
		AccessToken:            token,
		SecurityOAuthToken:     token,
		RefreshToken:           String(payload, "refresh_token"),
		UserID:                 firstMetadataString(payload, "user_id", "userId", "uid"),
		Name:                   firstMetadataString(payload, "user_name", "userName", "name"),
		MachineID:              machineID,
		MachineToken:           firstMetadataString(payload, "machine_token", "machineToken"),
		MachineType:            firstMetadataString(payload, "machine_type", "machineType"),
		MachineCode:            firstMetadataString(payload, "machine_code", "machineCode"),
		ExpireTime:             tokenExpiry(payload, now, "expires_at", "expiresAt", "expire_time", "expireTime", "expires_in", "expiresIn"),
		RefreshTokenExpireTime: tokenExpiry(payload, now, "refresh_token_expires_at", "refreshTokenExpiresAt", "refresh_token_expire_time", "refreshTokenExpireTime", "refresh_token_expires_in", "refreshTokenExpiresIn"),
		LastRefresh:            now.UTC().Format(time.RFC3339),
		RefreshIntervalSeconds: 1800,
	}
	if storage.ExpireTime > 0 {
		storage.Expired = time.Unix(storage.ExpireTime, 0).UTC().Format(time.RFC3339)
	}
	if storage.RefreshTokenExpireTime > 0 {
		storage.RefreshTokenExpired = time.Unix(storage.RefreshTokenExpireTime, 0).UTC().Format(time.RFC3339)
	}
	return storage, false, nil
}

// Refresh rotates a device token through the current official OpenAPI endpoint.
func (c *Client) Refresh(ctx context.Context, storage *TokenStorage) (*TokenStorage, error) {
	if storage == nil || strings.TrimSpace(storage.RefreshToken) == "" {
		return nil, fmt.Errorf("qoder: refresh token is missing")
	}
	if storage.RefreshTokenExpireTime > 0 && storage.RefreshTokenExpireTime <= c.now().Unix() {
		return nil, fmt.Errorf("qoder: refresh token has expired")
	}
	payload := map[string]string{"refresh_token": storage.RefreshToken}
	if storage.MachineID != "" {
		payload["machine_id"] = storage.MachineID
	}
	if storage.MachineType != "" {
		payload["machine_type"] = storage.MachineType
	}
	if storage.MachineCode != "" {
		payload["machine_code"] = storage.MachineCode
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoints.DeviceRefreshURL(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.applyOpenAPIHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	status, responseBody, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("qoder: token refresh: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("qoder: token refresh returned HTTP %d: %s", status, responseSummary(responseBody))
	}
	response, err := decodeObject(responseBody)
	if err != nil {
		return nil, fmt.Errorf("qoder: decode refreshed token: %w", err)
	}
	response = unwrapPayload(response)
	token := firstMetadataString(response, "device_token", "token", "access_token")
	if token == "" {
		return nil, fmt.Errorf("qoder: refresh response has no token")
	}
	updated := *storage
	updated.AccessToken = token
	updated.SecurityOAuthToken = token
	if refresh := String(response, "refresh_token"); refresh != "" {
		updated.RefreshToken = refresh
	}
	now := c.now()
	if expiry := tokenExpiry(response, now, "expires_at", "expiresAt", "expire_time", "expireTime", "expires_in", "expiresIn"); expiry > 0 {
		updated.ExpireTime = expiry
		updated.Expired = time.Unix(expiry, 0).UTC().Format(time.RFC3339)
	}
	if expiry := tokenExpiry(response, now, "refresh_token_expires_at", "refreshTokenExpiresAt", "refresh_token_expire_time", "refreshTokenExpireTime", "refresh_token_expires_in", "refreshTokenExpiresIn"); expiry > 0 {
		updated.RefreshTokenExpireTime = expiry
		updated.RefreshTokenExpired = time.Unix(expiry, 0).UTC().Format(time.RFC3339)
	}
	updated.LastRefresh = now.UTC().Format(time.RFC3339)
	return &updated, nil
}

func (c *Client) populateUserInfo(ctx context.Context, storage *TokenStorage) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoints.UserInfoURL(), nil)
	if err != nil {
		return err
	}
	c.applyOpenAPIHeaders(req)
	req.Header.Set("Authorization", "Bearer "+storage.AccessToken)
	status, body, err := c.do(req)
	if err != nil {
		return fmt.Errorf("qoder: userinfo request: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("qoder: userinfo returned HTTP %d: %s", status, responseSummary(body))
	}
	payload, err := decodeObject(body)
	if err != nil {
		return fmt.Errorf("qoder: decode userinfo: %w", err)
	}
	payload = unwrapPayload(payload)
	if userID := firstMetadataString(payload, "id", "user_id", "userId", "uid"); userID != "" {
		if storage.UserID != "" && storage.UserID != userID {
			return fmt.Errorf("qoder: authorized account does not match device token")
		}
		storage.UserID = userID
	}
	if storage.UserID == "" {
		return fmt.Errorf("qoder: userinfo returned no user id")
	}
	storage.Name = firstMetadataString(payload, "name", "username", "user_name")
	storage.Email = String(payload, "email")
	storage.OrganizationID = firstMetadataString(payload, "organization_id", "organizationId", "orgId")
	storage.OrganizationName = firstMetadataString(payload, "organization_name", "organizationName", "orgName")
	if organization, ok := payload["organization"].(map[string]any); ok {
		if storage.OrganizationID == "" {
			storage.OrganizationID = firstMetadataString(organization, "id", "org_id", "orgId")
		}
		if storage.OrganizationName == "" {
			storage.OrganizationName = firstMetadataString(organization, "name", "org_name", "orgName")
		}
	}
	storage.OrganizationTags = stringSlice(payload["organization_tags"])
	if agreed, exists := payload["data_policy_agreed"]; exists {
		storage.DataPolicyAgreed = boolValue(agreed)
	}
	return nil
}

func (c *Client) applyOpenAPIHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "qoder/"+ClientVersion)
}

func (c *Client) do(req *http.Request) (int, []byte, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("qoder: close auth response: %v", errClose)
		}
	}()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAuthResponseBytes+1))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if len(body) > maxAuthResponseBytes {
		return resp.StatusCode, nil, fmt.Errorf("qoder: response exceeds %d bytes", maxAuthResponseBytes)
	}
	return resp.StatusCode, body, nil
}

// NewAuthRecord builds the shared CLI and management persistence representation.
func NewAuthRecord(storage *TokenStorage) (*coreauth.Auth, error) {
	if storage == nil || storage.AccessToken == "" || storage.UserID == "" {
		return nil, fmt.Errorf("qoder: credential is incomplete")
	}
	storage.Region = NormalizeRegion(storage.Region)
	storage.Type = ProviderForRegion(storage.Region)
	storage.SecurityOAuthToken = storage.AccessToken
	if storage.RefreshIntervalSeconds <= 0 {
		storage.RefreshIntervalSeconds = 1800
	}
	provider := ProviderForRegion(storage.Region)
	fileName := fmt.Sprintf("%s-%d.json", provider, time.Now().UnixMilli())
	label := firstNonEmpty(storage.Email, storage.Name, storage.UserID, provider)
	metadata := map[string]any{"timestamp": time.Now().UnixMilli()}
	storage.ApplyMetadata(metadata)
	return &coreauth.Auth{
		ID:       fileName,
		Provider: provider,
		FileName: fileName,
		Label:    label,
		Storage:  storage,
		Metadata: metadata,
		Attributes: map[string]string{
			"auth_kind":       "oauth",
			"region":          storage.Region,
			"auth_index_seed": provider + ":" + storage.UserID,
		},
	}, nil
}

func tokenExpiry(payload map[string]any, now time.Time, keys ...string) int64 {
	for _, key := range keys {
		value, exists := payload[key]
		if !exists || value == nil {
			continue
		}
		if text, ok := value.(string); ok {
			if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(text)); err == nil {
				return parsed.Unix()
			}
		}
		numeric := int64Value(value)
		if numeric <= 0 {
			continue
		}
		isRelative := strings.Contains(strings.ToLower(key), "expires_in") || strings.Contains(strings.ToLower(key), "expiresin")
		if isRelative {
			// Some device-flow revisions returned milliseconds despite the field name.
			if numeric > 10_000_000 {
				numeric /= 1000
			}
			return now.Add(time.Duration(numeric) * time.Second).Unix()
		}
		if numeric > 10_000_000_000 {
			numeric /= 1000
		}
		return numeric
	}
	return 0
}

func parseExpiry(value string, relative int64, now time.Time) (time.Time, bool) {
	if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value)); err == nil {
		return parsed, true
	}
	if numeric := int64Value(value); numeric > 0 {
		if numeric > 10_000_000_000 {
			numeric /= 1000
		}
		return time.Unix(numeric, 0), true
	}
	if relative > 0 {
		return now.Add(time.Duration(relative) * time.Second), true
	}
	return time.Time{}, false
}

func decodeObject(body []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func unwrapPayload(payload map[string]any) map[string]any {
	current := payload
	for range 3 {
		if firstMetadataString(current, "token", "device_token", "access_token", "id", "user_id", "uid") != "" {
			return current
		}
		var nested map[string]any
		for _, key := range []string{"data", "result", "payload"} {
			if candidate, ok := current[key].(map[string]any); ok {
				nested = candidate
				break
			}
		}
		if nested == nil {
			break
		}
		current = nested
	}
	return current
}

func responseSummary(body []byte) string {
	text := strings.Join(strings.Fields(string(body)), " ")
	if len(text) > 300 {
		return text[:300] + "..."
	}
	return text
}

func randomURLSafe(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
