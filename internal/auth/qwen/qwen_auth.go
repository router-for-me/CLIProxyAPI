package qwen

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
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

const qwenAuthErrorBodyPreviewLimit = 256

const (
	qwenOAuthEnhancedUserAgent            = "QwenCode/0.14.2 (darwin; arm64)"
	qwenOAuthEnhancedRuntimeVersionHeader = "v22.17.0"
	qwenOAuthEnhancedPackageVersionHeader = "5.11.0"
	qwenOAuthEnhancedDashscopeAuthType    = "qwen-oauth"
	qwenOAuthEnhancedDashscopeCacheCtl    = "enable"
)

const (
	// QwenOAuthDeviceCodeEndpoint is the URL for initiating the OAuth 2.0 device authorization flow.
	QwenOAuthDeviceCodeEndpoint = "https://chat.qwen.ai/api/v1/oauth2/device/code"
	// QwenOAuthTokenEndpoint is the URL for exchanging device codes or refresh tokens for access tokens.
	QwenOAuthTokenEndpoint = "https://chat.qwen.ai/api/v1/oauth2/token"
	// QwenOAuthClientID is the client identifier for the Qwen OAuth 2.0 application.
	QwenOAuthClientID = "f0304373b74a44d2b584a3fb70ca9e56"
	// QwenOAuthScope defines the permissions requested by the application.
	QwenOAuthScope = "openid profile email model.completion"
	// QwenOAuthGrantType specifies the grant type for the device code flow.
	QwenOAuthGrantType = "urn:ietf:params:oauth:grant-type:device_code"
)

// QwenTokenData represents the OAuth credentials, including access and refresh tokens.
type QwenTokenData struct {
	AccessToken string `json:"access_token"`
	// RefreshToken is used to obtain a new access token when the current one expires.
	RefreshToken string `json:"refresh_token,omitempty"`
	// TokenType indicates the type of token, typically "Bearer".
	TokenType string `json:"token_type"`
	// ResourceURL specifies the base URL of the resource server.
	ResourceURL string `json:"resource_url,omitempty"`
	// Expire indicates the expiration date and time of the access token.
	Expire string `json:"expiry_date,omitempty"`
}

// DeviceFlow represents the response from the device authorization endpoint.
type DeviceFlow struct {
	// DeviceCode is the code that the client uses to poll for an access token.
	DeviceCode string `json:"device_code"`
	// UserCode is the code that the user enters at the verification URI.
	UserCode string `json:"user_code"`
	// VerificationURI is the URL where the user can enter the user code to authorize the device.
	VerificationURI string `json:"verification_uri"`
	// VerificationURIComplete is a URI that includes the user_code, which can be used to automatically
	// fill in the code on the verification page.
	VerificationURIComplete string `json:"verification_uri_complete"`
	// ExpiresIn is the time in seconds until the device_code and user_code expire.
	ExpiresIn int `json:"expires_in"`
	// Interval is the minimum time in seconds that the client should wait between polling requests.
	Interval int `json:"interval"`
	// CodeVerifier is the cryptographically random string used in the PKCE flow.
	CodeVerifier string `json:"code_verifier"`
}

// QwenTokenResponse represents the successful token response from the token endpoint.
type QwenTokenResponse struct {
	// AccessToken is the token used to access protected resources.
	AccessToken string `json:"access_token"`
	// RefreshToken is used to obtain a new access token.
	RefreshToken string `json:"refresh_token,omitempty"`
	// TokenType indicates the type of token, typically "Bearer".
	TokenType string `json:"token_type"`
	// ResourceURL specifies the base URL of the resource server.
	ResourceURL string `json:"resource_url,omitempty"`
	// ExpiresIn is the time in seconds until the access token expires.
	ExpiresIn int `json:"expires_in"`
}

// QwenAuth manages authentication and token handling for the Qwen API.
type QwenAuth struct {
	httpClient *http.Client
}

// NewQwenAuth creates a new QwenAuth instance with a proxy-configured HTTP client.
func NewQwenAuth(cfg *config.Config) *QwenAuth {
	return &QwenAuth{
		httpClient: util.SetProxy(&cfg.SDKConfig, &http.Client{}),
	}
}

// generateCodeVerifier generates a cryptographically random string for the PKCE code verifier.
func (qa *QwenAuth) generateCodeVerifier() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

// generateCodeChallenge creates a SHA-256 hash of the code verifier, used as the PKCE code challenge.
func (qa *QwenAuth) generateCodeChallenge(codeVerifier string) string {
	hash := sha256.Sum256([]byte(codeVerifier))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

// generatePKCEPair creates a new code verifier and its corresponding code challenge for PKCE.
func (qa *QwenAuth) generatePKCEPair() (string, string, error) {
	codeVerifier, err := qa.generateCodeVerifier()
	if err != nil {
		return "", "", err
	}
	codeChallenge := qa.generateCodeChallenge(codeVerifier)
	return codeVerifier, codeChallenge, nil
}

func isLikelyHTMLResponse(contentType string, body []byte) bool {
	if strings.Contains(strings.ToLower(contentType), "text/html") {
		return true
	}

	trimmed := bytes.TrimSpace(bytes.ToLower(body))
	if len(trimmed) == 0 {
		return false
	}

	return bytes.HasPrefix(trimmed, []byte("<!doctype html")) || bytes.HasPrefix(trimmed, []byte("<html")) || trimmed[0] == '<'
}

func isLikelyJSONBody(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return false
	}

	switch trimmed[0] {
	case '{', '[', '"', 't', 'f', 'n', '-':
		return true
	}

	return trimmed[0] >= '0' && trimmed[0] <= '9'
}

func summarizeResponseForError(resp *http.Response, body []byte) string {
	bodyPreview := strings.TrimSpace(string(body))
	if bodyPreview == "" {
		bodyPreview = "empty response body"
	}
	if len(bodyPreview) > qwenAuthErrorBodyPreviewLimit {
		bodyPreview = bodyPreview[:qwenAuthErrorBodyPreviewLimit] + "..."
	}

	contentType := ""
	statusCode := 0
	status := ""
	finalURL := ""
	if resp != nil {
		statusCode = resp.StatusCode
		status = resp.Status
		contentType = resp.Header.Get("Content-Type")
		if resp.Request != nil && resp.Request.URL != nil {
			finalURL = resp.Request.URL.String()
		}
	}

	if finalURL != "" {
		return fmt.Sprintf("status=%d status_text=%q content_type=%q final_url=%q body=%q", statusCode, status, contentType, finalURL, bodyPreview)
	}

	return fmt.Sprintf("status=%d status_text=%q content_type=%q body=%q", statusCode, status, contentType, bodyPreview)
}

func containsQwenWAFMarker(body []byte) bool {
	lowerBody := strings.ToLower(string(body))
	return strings.Contains(lowerBody, "aliyun_waf_") || strings.Contains(lowerBody, "captcha") || strings.Contains(lowerBody, "waf")
}

func shouldRetryWithEnhancedHeaders(resp *http.Response, body []byte, retryOn2xxNonJSON bool) bool {
	if resp == nil {
		return false
	}

	if isLikelyHTMLResponse(resp.Header.Get("Content-Type"), body) || containsQwenWAFMarker(body) {
		return true
	}

	if retryOn2xxNonJSON && resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices && !isLikelyJSONBody(body) {
		return true
	}

	switch resp.StatusCode {
	case http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return true
	}

	return false
}

func applyQwenOAuthRequestHeaders(req *http.Request, contentType string, includeRequestID bool, enhanced bool) {
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	if includeRequestID {
		req.Header.Set("x-request-id", uuid.NewString())
	}

	if !enhanced {
		return
	}

	req.Header.Set("User-Agent", qwenOAuthEnhancedUserAgent)
	req.Header.Set("X-Stainless-Runtime-Version", qwenOAuthEnhancedRuntimeVersionHeader)
	req.Header.Set("X-Stainless-Lang", "js")
	req.Header.Set("Accept-Language", "*")
	req.Header.Set("X-Dashscope-Cachecontrol", qwenOAuthEnhancedDashscopeCacheCtl)
	req.Header.Set("X-Stainless-Os", "MacOS")
	req.Header.Set("X-Dashscope-Authtype", qwenOAuthEnhancedDashscopeAuthType)
	req.Header.Set("X-Stainless-Arch", "arm64")
	req.Header.Set("X-Stainless-Runtime", "node")
	req.Header.Set("X-Stainless-Retry-Count", "0")
	req.Header.Set("X-Stainless-Package-Version", qwenOAuthEnhancedPackageVersionHeader)
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("X-Dashscope-Useragent", qwenOAuthEnhancedUserAgent)
}

// RefreshTokens exchanges a refresh token for a new access token.
func (qa *QwenAuth) RefreshTokens(ctx context.Context, refreshToken string) (*QwenTokenData, error) {
	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", refreshToken)
	data.Set("client_id", QwenOAuthClientID)

	req, err := http.NewRequestWithContext(ctx, "POST", QwenOAuthTokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create token request: %w", err)
	}

	applyQwenOAuthRequestHeaders(req, "application/x-www-form-urlencoded", false, false)

	resp, err := qa.httpClient.Do(req)

	// resp, err := qa.httpClient.PostForm(QwenOAuthTokenEndpoint, data)
	if err != nil {
		return nil, fmt.Errorf("token refresh request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if shouldRetryWithEnhancedHeaders(resp, body, true) {
		log.Warnf("qwen oauth refresh got suspicious upstream response, retrying with enhanced headers: %s", summarizeResponseForError(resp, body))

		enhancedReq, errBuild := http.NewRequestWithContext(ctx, "POST", QwenOAuthTokenEndpoint, strings.NewReader(data.Encode()))
		if errBuild != nil {
			log.Warnf("qwen oauth refresh enhanced retry skipped: failed to build request: %v", errBuild)
		} else {
			applyQwenOAuthRequestHeaders(enhancedReq, "application/x-www-form-urlencoded", false, true)
			enhancedResp, errDo := qa.httpClient.Do(enhancedReq)
			if errDo != nil {
				log.Warnf("qwen oauth refresh enhanced retry failed: %v", errDo)
			} else {
				defer func() {
					_ = enhancedResp.Body.Close()
				}()
				enhancedBody, errRead := io.ReadAll(enhancedResp.Body)
				if errRead != nil {
					log.Warnf("qwen oauth refresh enhanced retry read failed: %v", errRead)
				} else {
					resp = enhancedResp
					body = enhancedBody
				}
			}
		}
	}

	if resp.StatusCode != http.StatusOK {
		var errorData map[string]interface{}
		if err = json.Unmarshal(body, &errorData); err == nil {
			return nil, fmt.Errorf("token refresh failed: %v - %v", errorData["error"], errorData["error_description"])
		}
		return nil, fmt.Errorf("token refresh failed: unexpected upstream response (%s)", summarizeResponseForError(resp, body))
	}

	if isLikelyHTMLResponse(resp.Header.Get("Content-Type"), body) || !isLikelyJSONBody(body) {
		return nil, fmt.Errorf("token refresh failed: expected JSON response but received non-JSON content (%s)", summarizeResponseForError(resp, body))
	}

	var tokenData QwenTokenResponse
	if err = json.Unmarshal(body, &tokenData); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	return &QwenTokenData{
		AccessToken:  tokenData.AccessToken,
		TokenType:    tokenData.TokenType,
		RefreshToken: tokenData.RefreshToken,
		ResourceURL:  tokenData.ResourceURL,
		Expire:       time.Now().Add(time.Duration(tokenData.ExpiresIn) * time.Second).Format(time.RFC3339),
	}, nil
}

// InitiateDeviceFlow starts the OAuth 2.0 device authorization flow and returns the device flow details.
func (qa *QwenAuth) InitiateDeviceFlow(ctx context.Context) (*DeviceFlow, error) {
	// Generate PKCE code verifier and challenge
	codeVerifier, codeChallenge, err := qa.generatePKCEPair()
	if err != nil {
		return nil, fmt.Errorf("failed to generate PKCE pair: %w", err)
	}

	data := url.Values{}
	data.Set("client_id", QwenOAuthClientID)
	data.Set("scope", QwenOAuthScope)
	data.Set("code_challenge", codeChallenge)
	data.Set("code_challenge_method", "S256")

	req, err := http.NewRequestWithContext(ctx, "POST", QwenOAuthDeviceCodeEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create token request: %w", err)
	}

	applyQwenOAuthRequestHeaders(req, "application/x-www-form-urlencoded", true, false)

	resp, err := qa.httpClient.Do(req)

	// resp, err := qa.httpClient.PostForm(QwenOAuthDeviceCodeEndpoint, data)
	if err != nil {
		return nil, fmt.Errorf("device authorization request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if shouldRetryWithEnhancedHeaders(resp, body, true) {
		log.Warnf("qwen oauth device flow got suspicious upstream response, retrying with enhanced headers: %s", summarizeResponseForError(resp, body))

		enhancedReq, errBuild := http.NewRequestWithContext(ctx, "POST", QwenOAuthDeviceCodeEndpoint, strings.NewReader(data.Encode()))
		if errBuild != nil {
			log.Warnf("qwen oauth device flow enhanced retry skipped: failed to build request: %v", errBuild)
		} else {
			applyQwenOAuthRequestHeaders(enhancedReq, "application/x-www-form-urlencoded", true, true)
			enhancedResp, errDo := qa.httpClient.Do(enhancedReq)
			if errDo != nil {
				log.Warnf("qwen oauth device flow enhanced retry failed: %v", errDo)
			} else {
				defer func() {
					_ = enhancedResp.Body.Close()
				}()
				enhancedBody, errRead := io.ReadAll(enhancedResp.Body)
				if errRead != nil {
					log.Warnf("qwen oauth device flow enhanced retry read failed: %v", errRead)
				} else {
					resp = enhancedResp
					body = enhancedBody
				}
			}
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device authorization failed: unexpected upstream response (%s)", summarizeResponseForError(resp, body))
	}

	if isLikelyHTMLResponse(resp.Header.Get("Content-Type"), body) || !isLikelyJSONBody(body) {
		return nil, fmt.Errorf("device authorization failed: expected JSON response but received non-JSON content (%s)", summarizeResponseForError(resp, body))
	}

	var result DeviceFlow
	if err = json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse device flow response: %w", err)
	}

	// Check if the response indicates success
	if result.DeviceCode == "" {
		return nil, fmt.Errorf("device authorization failed: device_code not found in response")
	}

	// Add the code_verifier to the result so it can be used later for polling
	result.CodeVerifier = codeVerifier

	return &result, nil
}

// PollForToken polls the token endpoint with the device code to obtain an access token.
func (qa *QwenAuth) PollForToken(deviceCode, codeVerifier string) (*QwenTokenData, error) {
	pollInterval := 5 * time.Second
	maxAttempts := 60 // 5 minutes max

	for attempt := 0; attempt < maxAttempts; attempt++ {
		data := url.Values{}
		data.Set("grant_type", QwenOAuthGrantType)
		data.Set("client_id", QwenOAuthClientID)
		data.Set("device_code", deviceCode)
		data.Set("code_verifier", codeVerifier)

		req, err := http.NewRequest("POST", QwenOAuthTokenEndpoint, strings.NewReader(data.Encode()))
		if err != nil {
			fmt.Printf("Polling attempt %d/%d failed: %v\n", attempt+1, maxAttempts, err)
			time.Sleep(pollInterval)
			continue
		}
		applyQwenOAuthRequestHeaders(req, "application/x-www-form-urlencoded", false, false)

		resp, err := qa.httpClient.Do(req)
		if err != nil {
			fmt.Printf("Polling attempt %d/%d failed: %v\n", attempt+1, maxAttempts, err)
			time.Sleep(pollInterval)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			fmt.Printf("Polling attempt %d/%d failed: %v\n", attempt+1, maxAttempts, err)
			time.Sleep(pollInterval)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			// Parse the response as JSON to check for OAuth RFC 8628 standard errors
			var errorData map[string]interface{}
			if err = json.Unmarshal(body, &errorData); err == nil {
				// According to OAuth RFC 8628, handle standard polling responses
				if resp.StatusCode == http.StatusBadRequest {
					errorType, _ := errorData["error"].(string)
					switch errorType {
					case "authorization_pending":
						// User has not yet approved the authorization request. Continue polling.
						fmt.Printf("Polling attempt %d/%d...\n\n", attempt+1, maxAttempts)
						time.Sleep(pollInterval)
						continue
					case "slow_down":
						// Client is polling too frequently. Increase poll interval.
						pollInterval = time.Duration(float64(pollInterval) * 1.5)
						if pollInterval > 10*time.Second {
							pollInterval = 10 * time.Second
						}
						fmt.Printf("Server requested to slow down, increasing poll interval to %v\n\n", pollInterval)
						time.Sleep(pollInterval)
						continue
					case "expired_token":
						return nil, fmt.Errorf("device code expired. Please restart the authentication process")
					case "access_denied":
						return nil, fmt.Errorf("authorization denied by user. Please restart the authentication process")
					}
				}

				// For other errors, return with proper error information
				errorType, _ := errorData["error"].(string)
				errorDesc, _ := errorData["error_description"].(string)
				return nil, fmt.Errorf("device token poll failed: %s - %s", errorType, errorDesc)
			}

			if shouldRetryWithEnhancedHeaders(resp, body, false) {
				log.Warnf("qwen oauth token poll got suspicious upstream response, retrying with enhanced headers: %s", summarizeResponseForError(resp, body))

				enhancedReq, errBuild := http.NewRequest("POST", QwenOAuthTokenEndpoint, strings.NewReader(data.Encode()))
				if errBuild != nil {
					log.Warnf("qwen oauth token poll enhanced retry skipped: failed to build request: %v", errBuild)
				} else {
					applyQwenOAuthRequestHeaders(enhancedReq, "application/x-www-form-urlencoded", false, true)
					enhancedResp, errDo := qa.httpClient.Do(enhancedReq)
					if errDo != nil {
						log.Warnf("qwen oauth token poll enhanced retry failed: %v", errDo)
					} else {
						enhancedBody, errRead := io.ReadAll(enhancedResp.Body)
						_ = enhancedResp.Body.Close()
						if errRead != nil {
							log.Warnf("qwen oauth token poll enhanced retry read failed: %v", errRead)
						} else {
							resp = enhancedResp
							body = enhancedBody
						}
					}
				}
			}

			if resp.StatusCode != http.StatusOK {
				var retryErrorData map[string]interface{}
				if err = json.Unmarshal(body, &retryErrorData); err == nil {
					errorType, _ := retryErrorData["error"].(string)
					errorDesc, _ := retryErrorData["error_description"].(string)
					return nil, fmt.Errorf("device token poll failed: %s - %s", errorType, errorDesc)
				}

				// If JSON parsing fails, fall back to summarized text response
				return nil, fmt.Errorf("device token poll failed: unexpected upstream response (%s)", summarizeResponseForError(resp, body))
			}
		}

		if shouldRetryWithEnhancedHeaders(resp, body, true) {
			log.Warnf("qwen oauth token poll got suspicious upstream response, retrying with enhanced headers: %s", summarizeResponseForError(resp, body))

			enhancedReq, errBuild := http.NewRequest("POST", QwenOAuthTokenEndpoint, strings.NewReader(data.Encode()))
			if errBuild != nil {
				log.Warnf("qwen oauth token poll enhanced retry skipped: failed to build request: %v", errBuild)
			} else {
				applyQwenOAuthRequestHeaders(enhancedReq, "application/x-www-form-urlencoded", false, true)
				enhancedResp, errDo := qa.httpClient.Do(enhancedReq)
				if errDo != nil {
					log.Warnf("qwen oauth token poll enhanced retry failed: %v", errDo)
				} else {
					enhancedBody, errRead := io.ReadAll(enhancedResp.Body)
					_ = enhancedResp.Body.Close()
					if errRead != nil {
						log.Warnf("qwen oauth token poll enhanced retry read failed: %v", errRead)
					} else {
						resp = enhancedResp
						body = enhancedBody
					}
				}
			}
		}

		// log.Debugf("%s", string(body))
		if isLikelyHTMLResponse(resp.Header.Get("Content-Type"), body) || !isLikelyJSONBody(body) {
			return nil, fmt.Errorf("device token poll failed: expected JSON response but received non-JSON content (%s)", summarizeResponseForError(resp, body))
		}

		// Success - parse token data
		var response QwenTokenResponse
		if err = json.Unmarshal(body, &response); err != nil {
			return nil, fmt.Errorf("failed to parse token response: %w", err)
		}

		// Convert to QwenTokenData format and save
		tokenData := &QwenTokenData{
			AccessToken:  response.AccessToken,
			RefreshToken: response.RefreshToken,
			TokenType:    response.TokenType,
			ResourceURL:  response.ResourceURL,
			Expire:       time.Now().Add(time.Duration(response.ExpiresIn) * time.Second).Format(time.RFC3339),
		}

		return tokenData, nil
	}

	return nil, fmt.Errorf("authentication timeout. Please restart the authentication process")
}

// RefreshTokensWithRetry attempts to refresh tokens with a specified number of retries upon failure.
func (o *QwenAuth) RefreshTokensWithRetry(ctx context.Context, refreshToken string, maxRetries int) (*QwenTokenData, error) {
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

		tokenData, err := o.RefreshTokens(ctx, refreshToken)
		if err == nil {
			return tokenData, nil
		}

		lastErr = err
		log.Warnf("Token refresh attempt %d failed: %v", attempt+1, err)
	}

	return nil, fmt.Errorf("token refresh failed after %d attempts: %w", maxRetries, lastErr)
}

// CreateTokenStorage creates a QwenTokenStorage object from a QwenTokenData object.
func (o *QwenAuth) CreateTokenStorage(tokenData *QwenTokenData) *QwenTokenStorage {
	storage := &QwenTokenStorage{
		AccessToken:  tokenData.AccessToken,
		RefreshToken: tokenData.RefreshToken,
		LastRefresh:  time.Now().Format(time.RFC3339),
		ResourceURL:  tokenData.ResourceURL,
		Expire:       tokenData.Expire,
	}

	return storage
}

// UpdateTokenStorage updates an existing token storage with new token data
func (o *QwenAuth) UpdateTokenStorage(storage *QwenTokenStorage, tokenData *QwenTokenData) {
	storage.AccessToken = tokenData.AccessToken
	storage.RefreshToken = tokenData.RefreshToken
	storage.LastRefresh = time.Now().Format(time.RFC3339)
	storage.ResourceURL = tokenData.ResourceURL
	storage.Expire = tokenData.Expire
}
