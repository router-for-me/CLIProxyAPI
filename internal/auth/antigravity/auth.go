// Package antigravity provides OAuth2 authentication functionality for the Antigravity provider.
package antigravity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

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

// HTTPStatusError represents an HTTP error response with status code.
type HTTPStatusError struct {
	StatusCodeValue int
	Message         string
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

// GenerateState returns 16 random bytes as unpadded base64url, matching the
// official 22-character OAuth state.
func GenerateState() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("antigravity: generate state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// GeneratePKCE returns an RFC 7636 verifier and its S256 challenge. The
// official client always uses PKCE, even on the loopback flow.
func GeneratePKCE() (verifier, challenge string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("antigravity: generate PKCE verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func antigravityLoadCodeAssistMetadata() map[string]string {
	return map[string]string{
		"ideType": "ANTIGRAVITY",
	}
}

func antigravityControlPlaneMetadata(userAgent string) map[string]string {
	metadata := map[string]string{
		"ide_type":       "ANTIGRAVITY",
		"ide_version":    misc.AntigravityVersionFromUserAgent(userAgent),
		"ide_name":       "antigravity",
		"platform":       misc.AntigravityPlatformEnum(),
		"plugin_type":    "CLOUD_CODE",
		"update_channel": "stable",
	}
	return metadata
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

// BuildAuthURL generates the OAuth authorization URL. codeChallenge is the
// S256 challenge produced by GeneratePKCE; the official client sends PKCE and
// does not force prompt=consent.
func (o *AntigravityAuth) BuildAuthURL(state, redirectURI, codeChallenge string) string {
	if strings.TrimSpace(redirectURI) == "" {
		redirectURI = fmt.Sprintf("http://localhost:%d%s", CallbackPort, CallbackPath)
	}
	params := url.Values{}
	params.Set("access_type", "offline")
	params.Set("client_id", ClientID)
	if codeChallenge != "" {
		params.Set("code_challenge", codeChallenge)
		params.Set("code_challenge_method", "S256")
	}
	params.Set("redirect_uri", redirectURI)
	params.Set("response_type", "code")
	params.Set("scope", strings.Join(Scopes, " "))
	params.Set("state", state)
	return AuthEndpoint + "?" + params.Encode()
}

// ExchangeCodeForTokens exchanges authorization code for access and refresh tokens.
// codeVerifier must be the PKCE verifier paired with the authorization request.
func (o *AntigravityAuth) ExchangeCodeForTokens(ctx context.Context, code, redirectURI, codeVerifier string) (*TokenResponse, error) {
	data := url.Values{}
	data.Set("code", code)
	data.Set("client_id", ClientID)
	data.Set("client_secret", ClientSecret)
	if codeVerifier != "" {
		data.Set("code_verifier", codeVerifier)
	}
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

	bodyBytes, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return "", fmt.Errorf("read response: %w", errRead)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", &HTTPStatusError{
			StatusCodeValue: resp.StatusCode,
			Message:         fmt.Sprintf("request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes))),
		}
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
			return "", fmt.Errorf("project id not found in loadCodeAssist or onboardUser response")
		}
		return projectID, nil
	}

	return projectID, nil
}

// callControlPlane performs one authenticated v1internal POST with the same
// minimal header set the official language server uses.
func (o *AntigravityAuth) callControlPlane(ctx context.Context, accessToken, method string, body any) ([]byte, error) {
	rawBody, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal request body: %w", errMarshal)
	}
	endpointURL := fmt.Sprintf("%s/%s:%s", DailyAPIEndpoint, APIVersion, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, strings.NewReader(string(rawBody)))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", o.shortUserAgent())
	resp, errDo := o.httpClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("antigravity control plane: close body error: %v", errClose)
		}
	}()
	bodyBytes, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return nil, fmt.Errorf("read response: %w", errRead)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, &HTTPStatusError{
			StatusCodeValue: resp.StatusCode,
			Message:         fmt.Sprintf("request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes))),
		}
	}
	return bodyBytes, nil
}

// WarmupAfterLogin replays the post-login control-plane calls the official
// client makes right after a successful OAuth exchange. Errors are best effort:
// the login itself has already succeeded.
func (o *AntigravityAuth) WarmupAfterLogin(ctx context.Context, accessToken, projectID string) {
	if strings.TrimSpace(accessToken) == "" {
		return
	}
	if strings.TrimSpace(projectID) != "" {
		_, _ = o.callControlPlane(ctx, accessToken, "loadCodeAssist", map[string]any{
			"cloudaicompanionProject": projectID,
			"metadata":                antigravityLoadCodeAssistMetadata(),
		})
	}
	projectBody := map[string]any{}
	if strings.TrimSpace(projectID) != "" {
		projectBody["project"] = projectID
	}
	_, _ = o.callControlPlane(ctx, accessToken, "fetchUserInfo", projectBody)
	_, _ = o.callControlPlane(ctx, accessToken, "fetchAvailableModels", projectBody)
	_, _ = o.callControlPlane(ctx, accessToken, "listExperiments", map[string]any{
		"metadata": map[string]string{"updateChannel": "stable"},
	})
}

// OnboardUser attempts to fetch the project ID via onboardUser by polling for completion
func (o *AntigravityAuth) OnboardUser(ctx context.Context, accessToken, tierID string) (string, error) {
	log.Infof("Antigravity: onboarding user with tier: %s", tierID)
	userAgent := o.shortUserAgent()
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

		reqCtx := ctx
		var cancel context.CancelFunc
		if reqCtx == nil {
			reqCtx = context.Background()
		}
		reqCtx, cancel = context.WithTimeout(reqCtx, 30*time.Second)

		endpointURL := fmt.Sprintf("%s/%s:onboardUser", DailyAPIEndpoint, APIVersion)
		req, errRequest := http.NewRequestWithContext(reqCtx, http.MethodPost, endpointURL, strings.NewReader(string(rawBody)))
		if errRequest != nil {
			cancel()
			return "", fmt.Errorf("create request: %w", errRequest)
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", userAgent)

		resp, errDo := o.httpClient.Do(req)
		if errDo != nil {
			cancel()
			return "", fmt.Errorf("execute request: %w", errDo)
		}

		bodyBytes, errRead := io.ReadAll(resp.Body)
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
				projectID := ""
				if responseData, okResp := data["response"].(map[string]any); okResp {
					projectID = extractCloudaicompanionProject(responseData)
				}

				if projectID != "" {
					log.Infof("Successfully fetched project_id: %s", util.HideAPIKey(projectID))
					return projectID, nil
				}

				return "", fmt.Errorf("no project_id in response")
			}

			time.Sleep(2 * time.Second)
			continue
		}

		responsePreview := strings.TrimSpace(string(bodyBytes))
		if len(responsePreview) > 500 {
			responsePreview = responsePreview[:500]
		}

		responseErr := responsePreview
		if len(responseErr) > 200 {
			responseErr = responseErr[:200]
		}
		return "", &HTTPStatusError{
			StatusCodeValue: resp.StatusCode,
			Message:         fmt.Sprintf("http %d: %s", resp.StatusCode, responseErr),
		}
	}

	return "", fmt.Errorf("onboard user did not complete after %d attempts", maxAttempts)
}
