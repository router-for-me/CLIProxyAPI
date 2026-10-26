package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	log "github.com/sirupsen/logrus"
)

const DefaultRegion = "us-east-1"

var regionPattern = regexp.MustCompile(`^[a-z]{2}(?:-[a-z]+)+-[0-9]+$`)

// NormalizeMetadata accepts native Kiro credentials and account-manager exports.
// Explicit credential types and canonical field values take precedence.
func NormalizeMetadata(metadata map[string]any) {
	if metadata == nil {
		return
	}
	current := strings.ToLower(String(metadata, "type"))
	if current != "" && current != "kiro" {
		return
	}
	method := ""
	switch strings.ToLower(String(metadata, "provider")) {
	case "github", "google", "social":
		method = "social"
	case "builderid", "builder-id", "enterprise", "idc":
		method = "idc"
	case "external_idp", "external idp":
		method = "external_idp"
	}
	if current == "" && (method == "" || String(metadata, "refreshToken") == "" && String(metadata, "refresh_token") == "") {
		return
	}
	metadata["type"] = "kiro"
	for native, canonical := range map[string]string{
		"accessToken": "access_token", "refreshToken": "refresh_token", "expiresAt": "expired",
		"clientId": "client_id", "clientSecret": "client_secret", "profileArn": "profile_arn",
		"authMethod": "auth_method", "clientSecretExpiresAt": "client_secret_expires_at",
		"machineId": "machine_id", "startUrl": "start_url", "clientIdHash": "client_id_hash",
	} {
		if value, ok := metadata[native]; ok {
			if _, exists := metadata[canonical]; !exists {
				metadata[canonical] = value
			}
			delete(metadata, native)
		}
	}
	if String(metadata, "auth_method") == "" && method != "" {
		metadata["auth_method"] = method
	}
	EnsureMachineID(metadata)
}

func TokenNeedsRefresh(metadata map[string]any, now time.Time) bool {
	if String(metadata, "refresh_token") == "" {
		return false
	}
	if String(metadata, "access_token") == "" {
		return true
	}
	expires, err := time.Parse(time.RFC3339, String(metadata, "expired"))
	return err != nil || !expires.After(now.Add(5*time.Minute))
}

func ProfileNeedsLookup(metadata map[string]any, now time.Time) bool {
	if String(metadata, "access_token") == "" || String(metadata, "profile_arn") != "" {
		return false
	}
	retryAt, err := time.Parse(time.RFC3339, String(metadata, "kiro_profile_retry_after"))
	return err != nil || !retryAt.After(now) || retryAt.After(now.Add(5*time.Minute))
}

func String(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func Region(metadata map[string]any) string {
	if region := String(metadata, "region"); regionPattern.MatchString(region) {
		return region
	}
	return DefaultRegion
}

func APIRegion(metadata map[string]any) string {
	if region := String(metadata, "api_region"); regionPattern.MatchString(region) {
		return region
	}
	if arn := strings.Split(String(metadata, "profile_arn"), ":"); len(arn) > 3 && regionPattern.MatchString(arn[3]) {
		return arn[3]
	}
	return Region(metadata)
}

func RuntimeURL(metadata map[string]any) string {
	if base := String(metadata, "base_url"); base != "" {
		return strings.TrimRight(base, "/")
	}
	return "https://runtime." + APIRegion(metadata) + ".kiro.dev"
}

func ControlURL(metadata map[string]any) string {
	if base := String(metadata, "control_url"); base != "" {
		return strings.TrimRight(base, "/")
	}
	return "https://management." + APIRegion(metadata) + ".kiro.dev"
}

type Client struct {
	HTTP *http.Client
	// OIDCURL and AuthURL are injectable for deterministic authentication tests.
	OIDCURL string
	AuthURL string
}

type DeviceAuthorization struct {
	DeviceCode              string `json:"deviceCode"`
	UserCode                string `json:"userCode"`
	VerificationURI         string `json:"verificationUri"`
	VerificationURIComplete string `json:"verificationUriComplete"`
	ExpiresIn               int    `json:"expiresIn"`
	Interval                int    `json:"interval"`
	ClientID                string `json:"-"`
	ClientSecret            string `json:"-"`
	ClientSecretExpiresAt   int64  `json:"-"`
	Region                  string `json:"-"`
}

type Token struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int    `json:"expiresIn"`
	ProfileARN   string `json:"profileArn"`
	IDToken      string `json:"idToken"`
}

type APIError struct {
	Code    int
	Kind    string
	Message string
}

func (e *APIError) Error() string   { return fmt.Sprintf("kiro: %s (HTTP %d)", e.Kind, e.Code) }
func (e *APIError) StatusCode() int { return e.Code }

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) oidcURL(region string) string {
	if c.OIDCURL != "" {
		return strings.TrimRight(c.OIDCURL, "/")
	}
	return "https://oidc." + region + ".amazonaws.com"
}

func (c *Client) jsonRequest(ctx context.Context, method, endpoint, token string, extraHeaders map[string]string, body any, result any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "CLIProxyAPI/kiro")
	for key, value := range extraHeaders {
		req.Header.Set(key, value)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("kiro: close authentication response failed")
		}
	}()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure struct {
			Error   string `json:"error"`
			Type    string `json:"__type"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &failure)
		kind := failure.Error
		if kind == "" {
			kind = failure.Type
		}
		if kind == "" {
			kind = http.StatusText(resp.StatusCode)
		}
		return &APIError{Code: resp.StatusCode, Kind: kind, Message: failure.Message}
	}
	if err = json.Unmarshal(data, result); err != nil {
		return fmt.Errorf("kiro: invalid JSON response: %w", err)
	}
	return nil
}

func (c *Client) StartDeviceFlow(ctx context.Context, region, startURL string) (*DeviceAuthorization, error) {
	if !regionPattern.MatchString(region) {
		region = DefaultRegion
	}
	if startURL == "" {
		startURL = "https://view.awsapps.com/start"
	}
	var registration struct {
		ClientID              string `json:"clientId"`
		ClientSecret          string `json:"clientSecret"`
		ClientSecretExpiresAt int64  `json:"clientSecretExpiresAt"`
	}
	err := c.jsonRequest(ctx, http.MethodPost, c.oidcURL(region)+"/client/register", "", nil, map[string]any{
		"clientName": "Kiro IDE", "clientType": "public",
		"scopes":     []string{"codewhisperer:completions", "codewhisperer:analysis", "codewhisperer:conversations", "codewhisperer:transformations", "codewhisperer:taskassist"},
		"grantTypes": []string{"urn:ietf:params:oauth:grant-type:device_code", "refresh_token"},
	}, &registration)
	if err != nil {
		return nil, err
	}
	if registration.ClientID == "" || registration.ClientSecret == "" {
		return nil, fmt.Errorf("kiro: incomplete client registration")
	}
	device := &DeviceAuthorization{ClientID: registration.ClientID, ClientSecret: registration.ClientSecret, ClientSecretExpiresAt: registration.ClientSecretExpiresAt, Region: region}
	err = c.jsonRequest(ctx, http.MethodPost, c.oidcURL(region)+"/device_authorization", "", nil, map[string]any{
		"clientId": device.ClientID, "clientSecret": device.ClientSecret, "startUrl": startURL,
	}, device)
	if err != nil {
		return nil, err
	}
	if device.DeviceCode == "" || device.VerificationURI == "" && device.VerificationURIComplete == "" || device.ExpiresIn <= 0 {
		return nil, fmt.Errorf("kiro: incomplete device authorization")
	}
	return device, nil
}

func (c *Client) PollToken(ctx context.Context, device *DeviceAuthorization) (*Token, error) {
	var token Token
	err := c.jsonRequest(ctx, http.MethodPost, c.oidcURL(device.Region)+"/token", "", nil, map[string]any{
		"grantType": "urn:ietf:params:oauth:grant-type:device_code", "clientId": device.ClientID,
		"clientSecret": device.ClientSecret, "deviceCode": device.DeviceCode,
	}, &token)
	if err != nil {
		return nil, err
	}
	if token.AccessToken == "" {
		return nil, fmt.Errorf("kiro: empty access token")
	}
	return &token, nil
}

func (c *Client) WaitForAuthorization(ctx context.Context, device *DeviceAuthorization) (*Token, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(device.ExpiresIn)*time.Second)
	defer cancel()
	interval := time.Duration(device.Interval) * time.Second
	if interval < time.Second {
		interval = 5 * time.Second
	}
	for {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		token, err := c.PollToken(ctx, device)
		if err == nil {
			return token, nil
		}
		failure, ok := err.(*APIError)
		if !ok {
			return nil, err
		}
		switch failure.Kind {
		case "authorization_pending", "AuthorizationPendingException":
		case "slow_down", "SlowDownException":
			interval += 5 * time.Second
		default:
			return nil, err
		}
	}
}

func TokenMetadata(token *Token, device *DeviceAuthorization) map[string]any {
	metadata := map[string]any{"type": "kiro", "access_token": token.AccessToken, "refresh_token": token.RefreshToken,
		"client_id": device.ClientID, "client_secret": device.ClientSecret, "client_secret_expires_at": device.ClientSecretExpiresAt,
		"region": device.Region, "auth_method": "idc", "last_refresh": time.Now().UTC().Format(time.RFC3339)}
	ApplyToken(metadata, token)
	EnsureMachineID(metadata)
	return metadata
}

func ApplyToken(metadata map[string]any, token *Token) {
	metadata["access_token"] = token.AccessToken
	if token.RefreshToken != "" {
		metadata["refresh_token"] = token.RefreshToken
	}
	if token.ProfileARN != "" {
		metadata["profile_arn"] = token.ProfileARN
	}
	if token.ExpiresIn > 0 {
		metadata["expired"] = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}
	metadata["last_refresh"] = time.Now().UTC().Format(time.RFC3339)
}

func (c *Client) Refresh(ctx context.Context, metadata map[string]any) (*Token, error) {
	refresh := String(metadata, "refresh_token")
	if refresh == "" {
		return nil, fmt.Errorf("kiro: refresh token is missing")
	}
	method := strings.ToLower(String(metadata, "auth_method"))
	if strings.EqualFold(String(metadata, "provider"), "external_idp") || method == "external_idp" {
		return nil, fmt.Errorf("kiro: external identity provider token refresh is not supported")
	}
	// Social authentication uses the desktop auth service, independent of the inference region.
	endpoint := c.desktopAuthURL() + "/refreshToken"
	payload := map[string]any{"refreshToken": refresh}
	headers := map[string]string{"User-Agent": DesktopAuthUserAgent(metadata)}
	if clientID := String(metadata, "client_id"); clientID != "" {
		if String(metadata, "client_secret") == "" {
			return nil, fmt.Errorf("kiro: OIDC client secret is missing")
		}
		endpoint = c.oidcURL(Region(metadata)) + "/token"
		payload["grantType"], payload["clientId"], payload["clientSecret"] = "refresh_token", clientID, String(metadata, "client_secret")
		headers = nil
	} else if method == "idc" || method == "builderid" || method == "builder-id" || method == "enterprise" {
		return nil, fmt.Errorf("kiro: OIDC refresh requires client_id and client_secret")
	}
	var token Token
	if err := c.jsonRequest(ctx, http.MethodPost, endpoint, "", headers, payload, &token); err != nil {
		return nil, err
	}
	if token.AccessToken == "" {
		return nil, fmt.Errorf("kiro: refresh returned an empty access token")
	}
	return &token, nil
}

func (c *Client) controlPlaneRequest(ctx context.Context, target, token string, metadata map[string]any, body map[string]any, result any) error {
	if token == "" {
		return &APIError{Code: http.StatusUnauthorized, Kind: "missing_access_token"}
	}
	// Mirror the IDE control-plane client headers (captured from the official
	// client), including the per-request SDK invocation metadata.
	headers := map[string]string{
		"Content-Type":          "application/x-amz-json-1.0",
		"x-amz-target":          "KiroControlPlaneBearerService." + target,
		"User-Agent":            ControlPlaneUserAgent(metadata),
		"x-amz-user-agent":      ControlPlaneXAmzUserAgent(),
		"amz-sdk-invocation-id": uuid.NewString(),
		"amz-sdk-request":       "attempt=1; max=1",
		"Connection":            "close",
	}
	return c.jsonRequest(ctx, http.MethodPost, ControlURL(metadata)+"/", token, headers, body, result)
}

// ListProfiles reads the account's available profiles; the first ARN is the
// inference profile most accounts must reference.
func (c *Client) ListProfiles(ctx context.Context, metadata map[string]any) ([]string, error) {
	var result struct {
		Profiles []struct {
			ARN string `json:"arn"`
		} `json:"profiles"`
	}
	if err := c.controlPlaneRequest(ctx, "ListAvailableProfiles", String(metadata, "access_token"), metadata, map[string]any{}, &result); err != nil {
		return nil, err
	}
	arns := make([]string, 0, len(result.Profiles))
	for _, profile := range result.Profiles {
		if profile.ARN != "" {
			arns = append(arns, profile.ARN)
		}
	}
	return arns, nil
}

// ResolveProfileARN returns the stored profile ARN, or the first available
// profile ARN. An empty result without error means the account exposes none.
func (c *Client) ResolveProfileARN(ctx context.Context, metadata map[string]any) (string, error) {
	if arn := String(metadata, "profile_arn"); arn != "" {
		return arn, nil
	}
	arns, err := c.ListProfiles(ctx, metadata)
	if err != nil {
		return "", err
	}
	if len(arns) == 0 {
		return "", nil
	}
	return arns[0], nil
}

func (c *Client) ListModels(ctx context.Context, metadata map[string]any) ([]*registry.ModelInfo, error) {
	body := map[string]any{"origin": "AI_EDITOR"}
	if arn := String(metadata, "profile_arn"); arn != "" {
		body["profileArn"] = arn
	}
	models := make([]*registry.ModelInfo, 0)
	seenModels, seenPages := make(map[string]bool), make(map[string]bool)
	for {
		var result struct {
			Models []struct {
				ID           string   `json:"modelId"`
				Name         string   `json:"modelName"`
				Description  string   `json:"description"`
				Context      int      `json:"contextWindow"`
				Capabilities []string `json:"capabilities"`
				InputTypes   []string `json:"supportedInputTypes"`
				TokenLimits  struct {
					Input  int `json:"maxInputTokens"`
					Output int `json:"maxOutputTokens"`
				} `json:"tokenLimits"`
			} `json:"models"`
			Next string `json:"nextToken"`
		}
		if err := c.controlPlaneRequest(ctx, "ListAvailableModels", String(metadata, "access_token"), metadata, body, &result); err != nil {
			return nil, err
		}
		if result.Models == nil {
			return nil, fmt.Errorf("kiro: model response is missing its catalog")
		}
		for _, item := range result.Models {
			id := strings.TrimSpace(item.ID)
			if id == "" || seenModels[id] {
				continue
			}
			seenModels[id] = true
			inputTypes := item.InputTypes
			if len(inputTypes) == 0 {
				inputTypes = item.Capabilities
			}
			modality := make([]string, 0, len(inputTypes))
			seenTypes := make(map[string]bool)
			for _, capability := range inputTypes {
				value := strings.ToLower(strings.TrimSpace(capability))
				if (value == "text" || value == "image") && !seenTypes[value] {
					modality = append(modality, value)
					seenTypes[value] = true
				}
			}
			contextLength := item.TokenLimits.Input
			if contextLength <= 0 {
				contextLength = item.Context
			}
			models = append(models, &registry.ModelInfo{ID: id, Object: "model", OwnedBy: "kiro", Type: "kiro", DisplayName: item.Name, Description: item.Description, ContextLength: contextLength, MaxCompletionTokens: item.TokenLimits.Output, SupportedInputModalities: modality})
		}
		if result.Next == "" {
			return models, nil
		}
		if seenPages[result.Next] {
			return nil, fmt.Errorf("kiro: repeated model pagination token")
		}
		seenPages[result.Next] = true
		body["nextToken"] = result.Next
	}
}
