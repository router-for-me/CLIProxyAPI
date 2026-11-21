// Package antigravity provides OAuth2 authentication functionality for the Antigravity provider.
package antigravity

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

// TokenResponse represents OAuth token response from Google
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

// userInfo represents Google user profile
type userInfo struct {
	Email string `json:"email"`
}

const (
	maxAntigravityAuthResponseBytes     = 1 << 20  // 1 MiB cap for 2xx JSON responses
	maxAntigravityAuthErrorBodyBytes    = 64 << 10 // 64 KiB cap for non-2xx raw error bodies
	maxAntigravityAuthErrorMessageBytes = 8 << 10  // 8 KiB cap for formatted HTTPStatusError messages
)

// readBoundedResponseBody reads up to maxBytes from body and returns an error
// if the upstream response exceeds the limit.
func readBoundedResponseBody(body io.Reader, maxBytes int64) ([]byte, error) {
	data, errRead := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if errRead != nil {
		return nil, errRead
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("response body exceeds %d bytes", maxBytes)
	}
	return data, nil
}

// truncateUTF8Bytes caps s to at most maxBytes without splitting a multi-byte
// UTF-8 rune, and strips any trailing incomplete UTF-8 sequence.
func truncateUTF8Bytes(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) > maxBytes {
		s = s[:maxBytes]
	}
	return trimIncompleteTrailingUTF8(s)
}

func trimIncompleteTrailingUTF8(s string) string {
	if len(s) == 0 {
		return s
	}
	i := len(s) - 1
	for i >= 0 && len(s)-1-i < utf8.UTFMax-1 && !utf8.RuneStart(s[i]) {
		i--
	}
	if i < 0 || !utf8.RuneStart(s[i]) {
		return s
	}
	if utf8.FullRuneInString(s[i:]) {
		return s
	}
	return s[:i]
}

// ErrProjectIDNotFound indicates that project discovery completed without
// yielding a usable project_id for the credential.
var ErrProjectIDNotFound = errors.New("antigravity: project id not found")

// HTTPStatusError represents an HTTP error response with status code.
type HTTPStatusError struct {
	StatusCodeValue int
	Message         string
	RetryAfterValue *time.Duration
	Body            []byte
}

func (e *HTTPStatusError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *HTTPStatusError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.StatusCodeValue
}

// RetryAfter exposes the upstream Retry-After hint so the conductor can record
// the provider-supplied recovery deadline on the result.
func (e *HTTPStatusError) RetryAfter() *time.Duration {
	if e == nil {
		return nil
	}
	return e.RetryAfterValue
}

// ResponseBody exposes the raw upstream HTTP response body when captured so
// callers can inspect structured error details without preview truncation.
func (e *HTTPStatusError) ResponseBody() []byte {
	if e == nil || len(e.Body) == 0 {
		return nil
	}
	return e.Body
}

// AntigravityAuth handles Antigravity OAuth authentication
type AntigravityAuth struct {
	httpClient *http.Client
}

// NewAntigravityAuth creates a new Antigravity auth service.
func NewAntigravityAuth(cfg *config.Config, httpClient *http.Client) *AntigravityAuth {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if httpClient != nil {
		return &AntigravityAuth{httpClient: httpClient}
	}
	return &AntigravityAuth{
		httpClient: util.SetProxy(&cfg.SDKConfig, &http.Client{}),
	}
}

func (o *AntigravityAuth) shortUserAgent() string {
	return misc.AntigravityRequestUserAgent("")
}

func (o *AntigravityAuth) nodeUserAgent() string {
	return misc.AntigravityOnboardUserUserAgent("")
}

func antigravityLoadCodeAssistMetadata() map[string]string {
	return map[string]string{
		"ideType": "ANTIGRAVITY",
	}
}

func antigravityControlPlaneMetadata(userAgent string) map[string]string {
	return map[string]string{
		"ide_type":    "ANTIGRAVITY",
		"ide_version": misc.AntigravityVersionFromUserAgent(userAgent),
		"ide_name":    "antigravity",
	}
}

func extractCloudaicompanionProject(data map[string]any) string {
	if data == nil {
		return ""
	}
	for _, key := range []string{"cloudaicompanionProject", "projectId", "project"} {
		switch value := data[key].(type) {
		case string:
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		case map[string]any:
			if id, ok := value["id"].(string); ok {
				if trimmed := strings.TrimSpace(id); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}

func defaultAntigravityTierID(loadResp map[string]any) string {
	if tiers, okTiers := loadResp["allowedTiers"].([]any); okTiers {
		for _, rawTier := range tiers {
			tier, okTier := rawTier.(map[string]any)
			if !okTier {
				continue
			}
			if isDefault, okDefault := tier["isDefault"].(bool); !okDefault || !isDefault {
				continue
			}
			if id, okID := tier["id"].(string); okID {
				if trimmed := strings.TrimSpace(id); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	if currentTier, okTier := loadResp["currentTier"].(map[string]any); okTier {
		if id, okID := currentTier["id"].(string); okID {
			if trimmed := strings.TrimSpace(id); trimmed != "" {
				return trimmed
			}
		}
	}
	return "free-tier"
}

// BuildAuthURL generates the OAuth authorization URL.
func (o *AntigravityAuth) BuildAuthURL(state, redirectURI string) string {
	if strings.TrimSpace(redirectURI) == "" {
		redirectURI = fmt.Sprintf("http://localhost:%d/oauth-callback", CallbackPort)
	}
	params := url.Values{}
	params.Set("access_type", "offline")
	params.Set("client_id", ClientID)
	params.Set("prompt", "consent")
	params.Set("redirect_uri", redirectURI)
	params.Set("response_type", "code")
	params.Set("scope", strings.Join(Scopes, " "))
	params.Set("state", state)
	return AuthEndpoint + "?" + params.Encode()
}

// ExchangeCodeForTokens exchanges authorization code for access and refresh tokens
func (o *AntigravityAuth) ExchangeCodeForTokens(ctx context.Context, code, redirectURI string) (*TokenResponse, error) {
	data := url.Values{}
	data.Set("code", code)
	data.Set("client_id", ClientID)
	data.Set("client_secret", ClientSecret)
	data.Set("redirect_uri", redirectURI)
	data.Set("grant_type", "authorization_code")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("antigravity token exchange: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, errDo := o.httpClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("antigravity token exchange: execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("antigravity token exchange: close body error: %v", errClose)
		}
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		bodyBytes, errRead := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		if errRead != nil {
			return nil, fmt.Errorf("antigravity token exchange: read response: %w", errRead)
		}
		body := strings.TrimSpace(string(bodyBytes))
		msg := fmt.Sprintf("antigravity token exchange: request failed: status %d", resp.StatusCode)
		if body != "" {
			msg = fmt.Sprintf("antigravity token exchange: request failed: status %d: %s", resp.StatusCode, body)
		}
		return nil, &HTTPStatusError{StatusCodeValue: resp.StatusCode, Message: msg}
	}

	var token TokenResponse
	if errDecode := json.NewDecoder(resp.Body).Decode(&token); errDecode != nil {
		return nil, fmt.Errorf("antigravity token exchange: decode response: %w", errDecode)
	}
	return &token, nil
}

// FetchUserInfo retrieves user email from Google
func (o *AntigravityAuth) FetchUserInfo(ctx context.Context, accessToken string) (string, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return "", fmt.Errorf("antigravity userinfo: missing access token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, UserInfoEndpoint, nil)
	if err != nil {
		return "", fmt.Errorf("antigravity userinfo: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", o.shortUserAgent())

	resp, errDo := o.httpClient.Do(req)
	if errDo != nil {
		return "", fmt.Errorf("antigravity userinfo: execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("antigravity userinfo: close body error: %v", errClose)
		}
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		bodyBytes, errRead := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		if errRead != nil {
			return "", fmt.Errorf("antigravity userinfo: read response: %w", errRead)
		}
		body := strings.TrimSpace(string(bodyBytes))
		msg := fmt.Sprintf("antigravity userinfo: request failed: status %d", resp.StatusCode)
		if body != "" {
			msg = fmt.Sprintf("antigravity userinfo: request failed: status %d: %s", resp.StatusCode, body)
		}
		return "", &HTTPStatusError{StatusCodeValue: resp.StatusCode, Message: msg}
	}
	var info userInfo
	if errDecode := json.NewDecoder(resp.Body).Decode(&info); errDecode != nil {
		return "", fmt.Errorf("antigravity userinfo: decode response: %w", errDecode)
	}
	email := strings.TrimSpace(info.Email)
	if email == "" {
		return "", fmt.Errorf("antigravity userinfo: response missing email")
	}
	return email, nil
}

// retryAfterFromHeaders parses the Retry-After response header. It accepts both
// the delta-seconds form and the absolute HTTP-date form, returning nil when the
// header is absent or unparsable.
func retryAfterFromHeaders(header http.Header) *time.Duration {
	if len(header) == 0 {
		return nil
	}
	raw := strings.TrimSpace(header.Get("Retry-After"))
	if raw == "" {
		return nil
	}
	if seconds, errParse := strconv.Atoi(raw); errParse == nil {
		if seconds < 0 {
			return nil
		}
		d := time.Duration(seconds) * time.Second
		return &d
	}
	if when, errParse := http.ParseTime(raw); errParse == nil {
		d := time.Until(when)
		if d <= 0 {
			return nil
		}
		return &d
	}
	return nil
}

// FetchProjectID retrieves the project ID for the authenticated user via loadCodeAssist
func (o *AntigravityAuth) FetchProjectID(ctx context.Context, accessToken string) (string, error) {
	userAgent := o.shortUserAgent()
	loadReqBody := map[string]any{
		"metadata": antigravityLoadCodeAssistMetadata(),
	}

	rawBody, errMarshal := json.Marshal(loadReqBody)
	if errMarshal != nil {
		return "", fmt.Errorf("marshal request body: %w", errMarshal)
	}

	endpointURL := fmt.Sprintf("%s/%s:loadCodeAssist", DailyAPIEndpoint, APIVersion)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, strings.NewReader(string(rawBody)))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, errDo := o.httpClient.Do(req)
	if errDo != nil {
		return "", fmt.Errorf("execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("antigravity loadCodeAssist: close body error: %v", errClose)
		}
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		bodyBytes, errRead := io.ReadAll(io.LimitReader(resp.Body, maxAntigravityAuthErrorBodyBytes))
		if errRead != nil {
			return "", fmt.Errorf("read response: %w", errRead)
		}
		responseErr := truncateUTF8Bytes(strings.TrimSpace(string(bodyBytes)), maxAntigravityAuthErrorMessageBytes)
		return "", &HTTPStatusError{
			StatusCodeValue: resp.StatusCode,
			Message:         fmt.Sprintf("request failed with status %d: %s", resp.StatusCode, responseErr),
			RetryAfterValue: retryAfterFromHeaders(resp.Header),
			Body:            append([]byte(nil), bodyBytes...),
		}
	}

	bodyBytes, errRead := readBoundedResponseBody(resp.Body, maxAntigravityAuthResponseBytes)
	if errRead != nil {
		return "", fmt.Errorf("read response: %w", errRead)
	}

	var loadResp map[string]any
	if errDecode := json.Unmarshal(bodyBytes, &loadResp); errDecode != nil {
		return "", fmt.Errorf("decode response: %w", errDecode)
	}

	projectID := extractCloudaicompanionProject(loadResp)

	if projectID == "" {
		projectID, err = o.OnboardUser(ctx, accessToken, defaultAntigravityTierID(loadResp))
		if err != nil {
			return "", err
		}
		if projectID == "" {
			return "", fmt.Errorf("%w in loadCodeAssist or onboardUser response", ErrProjectIDNotFound)
		}
		return projectID, nil
	}

	return projectID, nil
}

// OnboardUser attempts to fetch the project ID via onboardUser by polling for completion
func (o *AntigravityAuth) OnboardUser(ctx context.Context, accessToken, tierID string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	log.Infof("Antigravity: onboarding user with tier: %s", tierID)
	userAgent := o.nodeUserAgent()
	requestBody := map[string]any{
		"tier_id":  tierID,
		"metadata": antigravityControlPlaneMetadata(userAgent),
	}

	rawBody, errMarshal := json.Marshal(requestBody)
	if errMarshal != nil {
		return "", fmt.Errorf("marshal request body: %w", errMarshal)
	}

	maxAttempts := 5
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		log.Debugf("Polling attempt %d/%d", attempt, maxAttempts)

		reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)

		endpointURL := fmt.Sprintf("%s/%s:onboardUser", DailyAPIEndpoint, APIVersion)
		req, errRequest := http.NewRequestWithContext(reqCtx, http.MethodPost, endpointURL, strings.NewReader(string(rawBody)))
		if errRequest != nil {
			cancel()
			return "", fmt.Errorf("create request: %w", errRequest)
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("X-Goog-Api-Client", misc.AntigravityGoogAPIClientUA)

		resp, errDo := o.httpClient.Do(req)
		if errDo != nil {
			cancel()
			return "", fmt.Errorf("execute request: %w", errDo)
		}

		var bodyBytes []byte
		var errRead error
		if resp.StatusCode == http.StatusOK {
			bodyBytes, errRead = readBoundedResponseBody(resp.Body, maxAntigravityAuthResponseBytes)
		} else {
			bodyBytes, errRead = io.ReadAll(io.LimitReader(resp.Body, maxAntigravityAuthErrorBodyBytes))
		}
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("close body error: %v", errClose)
		}
		cancel()

		if errRead != nil {
			return "", fmt.Errorf("read response: %w", errRead)
		}

		if resp.StatusCode == http.StatusOK {
			var data map[string]any
			if errDecode := json.Unmarshal(bodyBytes, &data); errDecode != nil {
				return "", fmt.Errorf("decode response: %w", errDecode)
			}

			if done, okDone := data["done"].(bool); okDone && done {
				if errOp := onboardOperationError(data["error"], bodyBytes); errOp != nil {
					return "", errOp
				}

				projectID := ""
				if responseData, okResp := data["response"].(map[string]any); okResp {
					projectID = extractCloudaicompanionProject(responseData)
				}

				if projectID != "" {
					log.Infof("Successfully fetched project_id: %s", util.HideAPIKey(projectID))
					return projectID, nil
				}

				return "", fmt.Errorf("%w: no project_id in response", ErrProjectIDNotFound)
			}

			if attempt == maxAttempts {
				break
			}
			if errCtx := ctx.Err(); errCtx != nil {
				return "", errCtx
			}
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			case <-timer.C:
			}
			continue
		}

		responseErr := truncateUTF8Bytes(strings.TrimSpace(string(bodyBytes)), maxAntigravityAuthErrorMessageBytes)
		return "", &HTTPStatusError{
			StatusCodeValue: resp.StatusCode,
			Message:         fmt.Sprintf("http %d: %s", resp.StatusCode, responseErr),
			RetryAfterValue: retryAfterFromHeaders(resp.Header),
			Body:            append([]byte(nil), bodyBytes...),
		}
	}

	return "", fmt.Errorf("onboard user did not complete after %d attempts", maxAttempts)
}

func onboardOperationError(raw any, bodyBytes []byte) error {
	if raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case string:
		msg := strings.TrimSpace(v)
		if msg == "" {
			return nil
		}
		return fmt.Errorf("onboard user operation error: %s", msg)
	case map[string]any:
		if len(v) == 0 {
			return nil
		}
		var code int
		var status string
		var parts []string
		if codeVal, okCode := v["code"].(float64); okCode && codeVal != 0 {
			code = int(codeVal)
			parts = append(parts, fmt.Sprintf("code %d", code))
		}
		if statusVal, okStatus := v["status"].(string); okStatus {
			if trimmed := strings.TrimSpace(statusVal); trimmed != "" {
				status = trimmed
				parts = append(parts, trimmed)
			}
		}
		if msgVal, okMsg := v["message"].(string); okMsg {
			if trimmed := strings.TrimSpace(msgVal); trimmed != "" {
				parts = append(parts, trimmed)
			}
		}
		msg := "onboard user operation error"
		if len(parts) > 0 {
			msg = fmt.Sprintf("onboard user operation error (%s)", strings.Join(parts, ": "))
		} else if encoded, errMarshal := json.Marshal(v); errMarshal == nil && len(encoded) > 0 {
			msg = fmt.Sprintf("onboard user operation error: %s", strings.TrimSpace(string(encoded)))
		}
		if httpStatus := onboardOperationHTTPStatus(code, status); httpStatus > 0 {
			return &HTTPStatusError{
				StatusCodeValue: httpStatus,
				Message:         msg,
				Body:            append([]byte(nil), bodyBytes...),
			}
		}
		return fmt.Errorf("%s", msg)
	default:
		return fmt.Errorf("onboard user operation error: %v", raw)
	}
}

func onboardOperationHTTPStatus(code int, status string) int {
	switch {
	case code == 7 || code == http.StatusForbidden || strings.EqualFold(status, "PERMISSION_DENIED"):
		return http.StatusForbidden
	case code == 8 || code == http.StatusTooManyRequests || strings.EqualFold(status, "RESOURCE_EXHAUSTED"):
		return http.StatusTooManyRequests
	case code == 16 || code == http.StatusUnauthorized || strings.EqualFold(status, "UNAUTHENTICATED"):
		return http.StatusUnauthorized
	default:
		return 0
	}
}
