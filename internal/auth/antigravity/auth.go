// Package antigravity provides OAuth2 authentication functionality for the Antigravity provider.
package antigravity

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
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

// currentAntigravityTierID extracts loadResp["currentTier"]["id"] without any onboarding
// fallback, used when loadCodeAssist already returned an existing project directly.
func currentAntigravityTierID(loadResp map[string]any) string {
	if currentTier, okTier := loadResp["currentTier"].(map[string]any); okTier {
		if id, okID := currentTier["id"].(string); okID {
			return strings.TrimSpace(id)
		}
	}
	return ""
}

// BuildAuthURL generates the OAuth authorization URL along with the PKCE code verifier
// that must be passed back into ExchangeCodeForTokens for this same login attempt.
func (o *AntigravityAuth) BuildAuthURL(state, redirectURI string) (authURL, codeVerifier string, err error) {
	if strings.TrimSpace(redirectURI) == "" {
		redirectURI = fmt.Sprintf("http://localhost:%d/oauth-callback", CallbackPort)
	}
	pkce, errPKCE := codex.GeneratePKCECodes()
	if errPKCE != nil {
		return "", "", fmt.Errorf("generate PKCE codes: %w", errPKCE)
	}
	params := url.Values{}
	params.Set("access_type", "offline")
	params.Set("client_id", OAuthClientID())
	params.Set("code_challenge", pkce.CodeChallenge)
	params.Set("code_challenge_method", "S256")
	params.Set("prompt", "consent")
	params.Set("redirect_uri", redirectURI)
	params.Set("response_type", "code")
	params.Set("scope", strings.Join(Scopes, " "))
	params.Set("state", state)
	return AuthEndpoint + "?" + params.Encode(), pkce.CodeVerifier, nil
}

// ExchangeCodeForTokens exchanges authorization code for access and refresh tokens.
// codeVerifier is the PKCE verifier returned by BuildAuthURL for the same login attempt;
// the real Antigravity client's OAuth client requires it (client_secret alone isn't enough).
func (o *AntigravityAuth) ExchangeCodeForTokens(ctx context.Context, code, redirectURI, codeVerifier string) (*TokenResponse, error) {
	data := url.Values{}
	data.Set("code", code)
	data.Set("client_id", OAuthClientID())
	data.Set("client_secret", OAuthClientSecret())
	data.Set("redirect_uri", redirectURI)
	data.Set("grant_type", "authorization_code")
	if codeVerifier != "" {
		data.Set("code_verifier", codeVerifier)
	}

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

// FetchProjectID retrieves the project ID for the authenticated user via loadCodeAssist.
func (o *AntigravityAuth) FetchProjectID(ctx context.Context, accessToken string) (string, error) {
	result, err := o.FetchProjectIDWithTier(ctx, accessToken)
	return result.ProjectID, err
}

// ProjectDiscoveryResult carries the project ID and the resolved user tier from loadCodeAssist.
type ProjectDiscoveryResult struct {
	ProjectID string
	// Tier is the resolved tier ID (e.g. "standard-tier", "gcp-ge-standard-tier"). May be empty
	// when loadCodeAssist returns a project directly without reporting currentTier/allowedTiers.
	Tier string
	// Region is the GCP location the BAIC (Business AI Code) backend must be called in
	// (e.g. "us"). Only populated for enterprise-licensed accounts.
	Region string
}

// EnterpriseLicense describes a single Gemini Enterprise (BAIC) license entry returned by
// businessaicode's fetchLicenses endpoint.
type EnterpriseLicense struct {
	UserTier        string `json:"userTier"`
	TierDisplayName string `json:"tierDisplayName"`
	ProjectID       string `json:"projectId"`
	Location        string `json:"location"`
}

// fetchEnterpriseLicenses queries businessaicode's fetchLicenses endpoint, which reports any
// Gemini Enterprise (BAIC) licenses assigned to the authenticated user along with the GCP
// project/region generation traffic must be routed to. Individual/free accounts return an
// empty licenses array (or a non-2xx error); callers should treat both as "no enterprise
// license" and fall back to loadCodeAssist/onboardUser project discovery.
func (o *AntigravityAuth) fetchEnterpriseLicenses(ctx context.Context, accessToken string) ([]EnterpriseLicense, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, BAICLicensesEndpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", o.shortUserAgent())

	resp, errDo := o.httpClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("antigravity fetchLicenses: close body error: %v", errClose)
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

	var parsed struct {
		Licenses []EnterpriseLicense `json:"licenses"`
	}
	if errDecode := json.Unmarshal(bodyBytes, &parsed); errDecode != nil {
		return nil, fmt.Errorf("decode response: %w", errDecode)
	}
	return parsed.Licenses, nil
}

// FetchProjectIDWithTier behaves like FetchProjectID but also returns the resolved tier ID so
// callers can route enterprise-tier ("gcp-ge-*") accounts to the BAIC generation backend.
func (o *AntigravityAuth) FetchProjectIDWithTier(ctx context.Context, accessToken string) (ProjectDiscoveryResult, error) {
	// Enterprise (BAIC) licenses are assigned out-of-band and aren't reflected in
	// loadCodeAssist's allowedTiers/currentTier; check for one first since it's the
	// authoritative source for the enterprise project ID and required region.
	licenses, errLicenses := o.fetchEnterpriseLicenses(ctx, accessToken)
	if errLicenses != nil {
		log.Debugf("antigravity: fetchLicenses check failed (likely not an enterprise account): %v", errLicenses)
	} else if len(licenses) > 0 {
		license := licenses[0]
		projectID := strings.TrimSpace(license.ProjectID)
		if projectID != "" {
			log.Infof("antigravity: found Gemini Enterprise license %q for project %s", license.TierDisplayName, util.HideAPIKey(projectID))
			return ProjectDiscoveryResult{
				ProjectID: projectID,
				Tier:      strings.TrimSpace(license.UserTier),
				Region:    strings.TrimSpace(license.Location),
			}, nil
		}
	}

	userAgent := o.shortUserAgent()
	loadReqBody := map[string]any{
		"metadata": antigravityLoadCodeAssistMetadata(),
	}

	rawBody, errMarshal := json.Marshal(loadReqBody)
	if errMarshal != nil {
		return ProjectDiscoveryResult{}, fmt.Errorf("marshal request body: %w", errMarshal)
	}

	endpointURL := fmt.Sprintf("%s/%s:loadCodeAssist", APIEndpoint, APIVersion)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, strings.NewReader(string(rawBody)))
	if err != nil {
		return ProjectDiscoveryResult{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, errDo := o.httpClient.Do(req)
	if errDo != nil {
		return ProjectDiscoveryResult{}, fmt.Errorf("execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("antigravity loadCodeAssist: close body error: %v", errClose)
		}
	}()

	bodyBytes, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return ProjectDiscoveryResult{}, fmt.Errorf("read response: %w", errRead)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return ProjectDiscoveryResult{}, &HTTPStatusError{
			StatusCodeValue: resp.StatusCode,
			Message:         fmt.Sprintf("request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes))),
		}
	}

	var loadResp map[string]any
	if errDecode := json.Unmarshal(bodyBytes, &loadResp); errDecode != nil {
		return ProjectDiscoveryResult{}, fmt.Errorf("decode response: %w", errDecode)
	}

	tier := currentAntigravityTierID(loadResp)
	projectID := extractCloudaicompanionProject(loadResp)

	if projectID == "" {
		tier = defaultAntigravityTierID(loadResp)
		projectID, err = o.OnboardUser(ctx, accessToken, tier)
		if err != nil {
			return ProjectDiscoveryResult{}, err
		}
		if projectID == "" {
			return ProjectDiscoveryResult{}, fmt.Errorf("project id not found in loadCodeAssist or onboardUser response")
		}
		return ProjectDiscoveryResult{ProjectID: projectID, Tier: tier}, nil
	}

	return ProjectDiscoveryResult{ProjectID: projectID, Tier: tier}, nil
}

// OnboardUser attempts to fetch the project ID via onboardUser by polling for completion.
func (o *AntigravityAuth) OnboardUser(ctx context.Context, accessToken, tierID string) (string, error) {
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
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("X-Goog-Api-Client", misc.AntigravityGoogAPIClientUA)

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
