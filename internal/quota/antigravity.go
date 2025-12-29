package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	antigravityQuotaAPIURL     = "https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels"
	antigravityLoadProjectURL  = "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"
	antigravityAPIUserAgent    = "antigravity/1.11.3 Darwin/arm64"
	antigravityAPIClient       = "google-cloud-sdk vscode_cloudshelleditor/0.1"
	antigravityClientMetadata  = `{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}`
)

// AntigravityFetcher implements quota fetching for Antigravity and Gemini-CLI providers.
// Both providers use the same Google Cloud Code API for quota information.
type AntigravityFetcher struct {
	httpClient *http.Client
}

// NewAntigravityFetcher creates a new Antigravity quota fetcher.
func NewAntigravityFetcher(httpClient *http.Client) *AntigravityFetcher {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &AntigravityFetcher{httpClient: httpClient}
}

// Provider returns the primary provider name.
func (f *AntigravityFetcher) Provider() string {
	return "antigravity"
}

// SupportedProviders returns all provider names this fetcher supports.
func (f *AntigravityFetcher) SupportedProviders() []string {
	return []string{"antigravity", "gemini-cli"}
}

// CanFetch returns true if this fetcher can handle the given provider.
func (f *AntigravityFetcher) CanFetch(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	return provider == "antigravity" || provider == "gemini-cli"
}

// FetchQuota fetches quota for an Antigravity or Gemini-CLI auth credential.
func (f *AntigravityFetcher) FetchQuota(ctx context.Context, auth *coreauth.Auth) (*ProviderQuotaData, error) {
	if auth == nil {
		return nil, fmt.Errorf("auth is nil")
	}

	// Get access token from metadata
	accessToken := f.extractAccessToken(auth)
	if accessToken == "" {
		return UnavailableQuota("no access token available"), nil
	}

	// Get project ID from metadata or fetch it
	projectID := f.extractProjectID(auth)
	if projectID == "" {
		// Try to fetch project ID
		var err error
		projectID, err = f.fetchProjectID(ctx, accessToken)
		if err != nil {
			log.Warnf("antigravity quota: failed to fetch project ID: %v", err)
			return UnavailableQuota(fmt.Sprintf("failed to fetch project ID: %v", err)), nil
		}
	}

	// Fetch available models and quota info
	quotaData, err := f.fetchQuotaData(ctx, accessToken, projectID)
	if err != nil {
		log.Warnf("antigravity quota: failed to fetch quota: %v", err)
		return UnavailableQuota(fmt.Sprintf("failed to fetch quota: %v", err)), nil
	}

	return quotaData, nil
}

// extractAccessToken extracts the access token from auth metadata.
func (f *AntigravityFetcher) extractAccessToken(auth *coreauth.Auth) string {
	if auth.Metadata == nil {
		return ""
	}
	if token, ok := auth.Metadata["access_token"].(string); ok {
		return strings.TrimSpace(token)
	}
	return ""
}

// extractProjectID extracts the project ID from auth metadata.
func (f *AntigravityFetcher) extractProjectID(auth *coreauth.Auth) string {
	if auth.Metadata == nil {
		return ""
	}
	if pid, ok := auth.Metadata["project_id"].(string); ok {
		return strings.TrimSpace(pid)
	}
	return ""
}

// fetchProjectID fetches the project ID using the loadCodeAssist API.
func (f *AntigravityFetcher) fetchProjectID(ctx context.Context, accessToken string) (string, error) {
	// Reference: Antigravity-Manager uses just {"metadata": {"ideType": "ANTIGRAVITY"}}
	reqBody := map[string]any{
		"metadata": map[string]string{
			"ideType": "ANTIGRAVITY",
		},
	}

	rawBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, antigravityLoadProjectURL, strings.NewReader(string(rawBody)))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	f.setRequestHeaders(req, accessToken)

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	// Extract project ID
	if projectID, ok := result["cloudaicompanionProject"].(string); ok {
		return strings.TrimSpace(projectID), nil
	}
	if projectMap, ok := result["cloudaicompanionProject"].(map[string]any); ok {
		if id, okID := projectMap["id"].(string); okID {
			return strings.TrimSpace(id), nil
		}
	}

	return "", fmt.Errorf("no cloudaicompanionProject in response")
}

// fetchQuotaData fetches the quota data from the fetchAvailableModels API.
func (f *AntigravityFetcher) fetchQuotaData(ctx context.Context, accessToken, projectID string) (*ProviderQuotaData, error) {
	// Reference: Antigravity-Manager uses just {"project": "project-id"}
	reqBody := map[string]any{}
	if projectID != "" {
		reqBody["project"] = projectID
	}

	rawBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, antigravityQuotaAPIURL, strings.NewReader(string(rawBody)))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	f.setRequestHeaders(req, accessToken)

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode == http.StatusForbidden {
		return &ProviderQuotaData{
			Models:      []ModelQuota{},
			LastUpdated: time.Now(),
			IsForbidden: true,
			Error:       "account forbidden",
		}, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result fetchAvailableModelsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return f.parseQuotaResponse(&result), nil
}

// setRequestHeaders sets the standard headers for Antigravity API requests.
func (f *AntigravityFetcher) setRequestHeaders(req *http.Request, accessToken string) {
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", antigravityAPIUserAgent)
	req.Header.Set("X-Goog-Api-Client", antigravityAPIClient)
	req.Header.Set("Client-Metadata", antigravityClientMetadata)
}

// API response structures

type fetchAvailableModelsResponse struct {
	// Models is a map from model name to model info (not an array!)
	Models           map[string]modelInfo `json:"models"`
	CurrentTier      *tierInfo            `json:"currentTier"`
	AvailableTiers   []tierInfo           `json:"availableTiers"`
	ProjectID        string               `json:"cloudaicompanionProject"`
	TierUpgradeURL   string               `json:"tierUpgradeUrl"`
}

type modelInfo struct {
	// QuotaInfo contains remaining quota information
	QuotaInfo *quotaInfo `json:"quotaInfo"`
}

type quotaInfo struct {
	RemainingFraction *float64 `json:"remainingFraction"`
	ResetTime         string   `json:"resetTime"`
}

type tierInfo struct {
	TierID      string `json:"tierId"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	IsDefault   bool   `json:"isDefault"`
}

// parseQuotaResponse converts the API response to our unified ProviderQuotaData format.
func (f *AntigravityFetcher) parseQuotaResponse(resp *fetchAvailableModelsResponse) *ProviderQuotaData {
	var models []ModelQuota

	// Models is a map from model name to model info
	for modelName, modelInfo := range resp.Models {
		mq := ModelQuota{
			Name:       modelName,
			Percentage: -1, // Default to unavailable
		}

		if modelInfo.QuotaInfo != nil {
			if modelInfo.QuotaInfo.RemainingFraction != nil {
				// remainingFraction is 0-1, convert to percentage
				mq.Percentage = *modelInfo.QuotaInfo.RemainingFraction * 100
			}
			mq.ResetTime = modelInfo.QuotaInfo.ResetTime
		}

		models = append(models, mq)
	}

	data := &ProviderQuotaData{
		Models:      models,
		LastUpdated: time.Now(),
		IsForbidden: false,
	}

	// Set plan type from current tier
	if resp.CurrentTier != nil {
		data.PlanType = resp.CurrentTier.TierID
		data.Extra = map[string]any{
			"tier_name":        resp.CurrentTier.DisplayName,
			"tier_description": resp.CurrentTier.Description,
			"project_id":       resp.ProjectID,
		}
		if resp.TierUpgradeURL != "" {
			data.Extra["upgrade_url"] = resp.TierUpgradeURL
		}
	}

	return data
}

// GetSubscriptionInfo fetches subscription/tier information for an Antigravity account.
func (f *AntigravityFetcher) GetSubscriptionInfo(ctx context.Context, auth *coreauth.Auth) (*SubscriptionInfo, error) {
	if auth == nil {
		return nil, fmt.Errorf("auth is nil")
	}

	accessToken := f.extractAccessToken(auth)
	if accessToken == "" {
		return nil, fmt.Errorf("no access token available")
	}

	projectID := f.extractProjectID(auth)
	if projectID == "" {
		var err error
		projectID, err = f.fetchProjectID(ctx, accessToken)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch project ID: %w", err)
		}
	}

	quotaData, err := f.fetchQuotaData(ctx, accessToken, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch quota data: %w", err)
	}

	info := &SubscriptionInfo{
		ProjectID: projectID,
	}

	if quotaData.Extra != nil {
		if tierName, ok := quotaData.Extra["tier_name"].(string); ok {
			info.CurrentTier = &SubscriptionTier{
				ID:   quotaData.PlanType,
				Name: tierName,
			}
			if desc, ok := quotaData.Extra["tier_description"].(string); ok {
				info.CurrentTier.Description = desc
			}
		}
		if url, ok := quotaData.Extra["upgrade_url"].(string); ok {
			info.UpgradeURL = url
			if info.CurrentTier != nil {
				info.CurrentTier.UpgradeURL = url
			}
		}
	}

	return info, nil
}