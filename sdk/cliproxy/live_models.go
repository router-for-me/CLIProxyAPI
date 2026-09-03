package cliproxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	kimiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kimi"
	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// liveModelFetchTimeout bounds how long the proxy waits for a provider's native
// model list endpoint.
const liveModelFetchTimeout = 15 * time.Second

// openAIModelListResponse is the common OpenAI-style /v1/models payload.
type openAIModelListResponse struct {
	Object string                `json:"object"`
	Data   []openAIModelListItem `json:"data"`
}

type openAIModelListItem struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// geminiModelListResponse mirrors the Google Generative Language API /v1/models response.
type geminiModelListResponse struct {
	Models []geminiModelListItem `json:"models"`
}

type geminiModelListItem struct {
	Name                       string   `json:"name"`
	Version                    string   `json:"version"`
	DisplayName                string   `json:"displayName"`
	Description                string   `json:"description"`
	InputTokenLimit            int      `json:"inputTokenLimit"`
	OutputTokenLimit           int      `json:"outputTokenLimit"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
}

// liveModelsEnabled reports whether live provider model loading is enabled for
// the given auth. It mirrors the Cursor-specific check but applies to all
// native providers.
func (s *Service) liveModelsEnabled(auth *coreauth.Auth) bool {
	if s == nil || s.cfg == nil {
		return false
	}
	if !s.cfg.EnableLiveProviderModels {
		return false
	}
	if auth != nil && auth.Attributes != nil {
		switch strings.ToLower(strings.TrimSpace(auth.Attributes["live_models"])) {
		case "true", "1", "yes":
			return true
		case "false", "0", "no":
			return false
		}
	}
	return true
}

// fetchLiveModelsForAuth fetches the native model list for a built-in provider
// from its default upstream URL. It returns nil on failure so callers can fall
// back to the static catalog. Custom OpenAI-compatible providers are not
// supported here and always return nil.
func (s *Service) fetchLiveModelsForAuth(ctx context.Context, provider string, auth *coreauth.Auth) []*registry.ModelInfo {
	if auth == nil {
		return nil
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case "kimi":
		token := kimiTokenForLiveModels(auth)
		return s.fetchOpenAIModelsForAuth(ctx, auth, "kimi", kimiauth.KimiAPIBaseURL, bearerAuthHeader(token))
	case "xai":
		token, baseURL := xaiCredsForLiveModels(auth)
		if baseURL == "" {
			baseURL = xaiauth.DefaultAPIBaseURL
		}
		return s.fetchOpenAIModelsForAuth(ctx, auth, "xai", baseURL, bearerAuthHeader(token))
	case "claude":
		apiKey, baseURL := claudeCredsForLiveModels(auth)
		if baseURL == "" {
			baseURL = "https://api.anthropic.com"
		}
		return s.fetchOpenAIModelsForAuth(ctx, auth, "claude", baseURL, bearerAuthHeader(apiKey))
	case "codex":
		apiKey, baseURL := codexCredsForLiveModels(auth)
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		return s.fetchOpenAIModelsForAuth(ctx, auth, "openai", baseURL, bearerAuthHeader(apiKey))
	case "gemini", "gemini-interactions":
		baseURL := resolveGeminiBaseURLForLiveModels(auth)
		apiKey := geminiAPIKeyForLiveModels(auth)
		return s.fetchGeminiModelsForAuth(ctx, auth, "gemini", baseURL, apiKey)
	case "vertex":
		apiKey, baseURL := vertexCredsForLiveModels(auth)
		if baseURL == "" {
			baseURL = "https://aiplatform.googleapis.com"
		}
		return s.fetchGeminiModelsForAuth(ctx, auth, "vertex", baseURL, apiKey)
	case "aistudio":
		baseURL := resolveAIStudioBaseURLForLiveModels(auth)
		apiKey := geminiAPIKeyForLiveModels(auth)
		return s.fetchGeminiModelsForAuth(ctx, auth, "gemini", baseURL, apiKey)
	case "antigravity":
		return nil
	case "cursor":
		return s.fetchCursorModelsForAuth(ctx, auth)
	default:
		return nil
	}
}

// fetchOpenAIModelsForAuth performs a GET {baseURL}/models call and decodes an
// OpenAI-style model list. The authHeader value is the full Authorization header
// (including scheme) or empty string when no auth is available.
func (s *Service) fetchOpenAIModelsForAuth(ctx context.Context, auth *coreauth.Auth, modelType, baseURL string, authHeader string) []*registry.ModelInfo {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil
	}

	client := helps.NewProxyAwareHTTPClient(ctx, s.cfg, auth, liveModelFetchTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		log.Debugf("live models fetch %s: build request: %v", modelType, err)
		return nil
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Debugf("live models fetch %s: do request: %v", modelType, err)
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if errClose := resp.Body.Close(); errClose != nil {
		log.Debugf("live models fetch %s: close body: %v", modelType, errClose)
	}
	if err != nil {
		log.Debugf("live models fetch %s: read body: %v", modelType, err)
		return nil
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		log.Debugf("live models fetch %s: status %d: %s", modelType, resp.StatusCode, string(body[:min(len(body), 256)]))
		return nil
	}

	var parsed openAIModelListResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		log.Debugf("live models fetch %s: decode: %v", modelType, err)
		return nil
	}
	if len(parsed.Data) == 0 {
		return nil
	}

	now := time.Now().Unix()
	models := make([]*registry.ModelInfo, 0, len(parsed.Data))
	seen := make(map[string]struct{}, len(parsed.Data))
	for _, item := range parsed.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}

		created := item.Created
		if created <= 0 {
			created = now
		}
		models = append(models, &registry.ModelInfo{
			ID:          id,
			Object:      "model",
			Created:     created,
			OwnedBy:     strings.TrimSpace(item.OwnedBy),
			Type:        modelType,
			DisplayName: id,
		})
	}
	return models
}

// fetchGeminiModelsForAuth performs a GET {baseURL}/v1/models call and decodes
// the Google Generative Language API model list. For Vertex, the base URL
// already includes the publisher path fragment when apiKey auth is used.
func (s *Service) fetchGeminiModelsForAuth(ctx context.Context, auth *coreauth.Auth, modelType, baseURL, apiKey string) []*registry.ModelInfo {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil
	}

	url := baseURL + "/v1/models"
	client := helps.NewProxyAwareHTTPClient(ctx, s.cfg, auth, liveModelFetchTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		log.Debugf("live models fetch %s: build request: %v", modelType, err)
		return nil
	}
	if apiKey != "" {
		req.Header.Set("x-goog-api-key", apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Debugf("live models fetch %s: do request: %v", modelType, err)
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if errClose := resp.Body.Close(); errClose != nil {
		log.Debugf("live models fetch %s: close body: %v", modelType, errClose)
	}
	if err != nil {
		log.Debugf("live models fetch %s: read body: %v", modelType, err)
		return nil
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		log.Debugf("live models fetch %s: status %d: %s", modelType, resp.StatusCode, string(body[:min(len(body), 256)]))
		return nil
	}

	var parsed geminiModelListResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		log.Debugf("live models fetch %s: decode: %v", modelType, err)
		return nil
	}
	if len(parsed.Models) == 0 {
		return nil
	}

	now := time.Now().Unix()
	models := make([]*registry.ModelInfo, 0, len(parsed.Models))
	seen := make(map[string]struct{}, len(parsed.Models))
	for _, item := range parsed.Models {
		id := strings.TrimSpace(item.Name)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}

		displayName := strings.TrimSpace(item.DisplayName)
		if displayName == "" {
			displayName = id
		}
		models = append(models, &registry.ModelInfo{
			ID:                         id,
			Object:                     "model",
			Created:                    now,
			OwnedBy:                    "google",
			Type:                       modelType,
			Name:                       id,
			Version:                    strings.TrimSpace(item.Version),
			DisplayName:                displayName,
			Description:                strings.TrimSpace(item.Description),
			InputTokenLimit:            item.InputTokenLimit,
			OutputTokenLimit:           item.OutputTokenLimit,
			SupportedGenerationMethods: item.SupportedGenerationMethods,
		})
	}
	return models
}

// maybeMergeLiveModels fetches and merges the provider's native model list when
// live loading is enabled. On failure or when disabled it returns the static
// list unchanged.
func (s *Service) maybeMergeLiveModels(ctx context.Context, auth *coreauth.Auth, provider string, static []*registry.ModelInfo) []*registry.ModelInfo {
	if !s.liveModelsEnabled(auth) {
		return static
	}
	live := s.fetchLiveModelsForAuth(ctx, provider, auth)
	return mergeLiveModels(live, static, provider)
}

// mergeLiveModels combines live-fetched models with the static catalog, keeping
// the live order and augmenting any missing live entries with static metadata.
func mergeLiveModels(live, static []*registry.ModelInfo, provider string) []*registry.ModelInfo {
	if len(live) == 0 {
		return static
	}
	staticByID := make(map[string]*registry.ModelInfo, len(static))
	for _, m := range static {
		if m == nil {
			continue
		}
		staticByID[strings.ToLower(m.ID)] = m
	}

	out := make([]*registry.ModelInfo, 0, len(live))
	for _, m := range live {
		if m == nil {
			continue
		}
		if enriched, ok := staticByID[strings.ToLower(m.ID)]; ok {
			clone := *enriched
			if m.DisplayName != "" {
				clone.DisplayName = m.DisplayName
			}
			if m.OwnedBy != "" {
				clone.OwnedBy = m.OwnedBy
			}
			out = append(out, &clone)
			continue
		}
		if m.Type == "" {
			m.Type = provider
		}
		out = append(out, m)
	}
	return out
}

func bearerAuthHeader(token string) string {
	if token == "" {
		return ""
	}
	return "Bearer " + token
}

// kimiTokenForLiveModels extracts the Kimi access token for live model loading.
func kimiTokenForLiveModels(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["access_token"].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	if auth.Attributes != nil {
		for _, key := range []string{"access_token", "api_key"} {
			if v := strings.TrimSpace(auth.Attributes[key]); v != "" {
				return v
			}
		}
	}
	return ""
}

// xaiCredsForLiveModels extracts the xAI access token and optional base URL for
// live model loading.
func xaiCredsForLiveModels(auth *coreauth.Auth) (token, baseURL string) {
	if auth == nil {
		return "", ""
	}
	if auth.Attributes != nil {
		token = strings.TrimSpace(auth.Attributes["api_key"])
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
	}
	if token == "" && auth.Metadata != nil {
		if v, ok := auth.Metadata["access_token"].(string); ok {
			token = strings.TrimSpace(v)
		}
		if baseURL == "" {
			if v, ok := auth.Metadata["base_url"].(string); ok {
				baseURL = strings.TrimSpace(v)
			}
		}
	}
	return token, baseURL
}

// claudeCredsForLiveModels extracts the Claude API key and optional base URL for
// live model loading.
func claudeCredsForLiveModels(auth *coreauth.Auth) (apiKey, baseURL string) {
	if auth == nil {
		return "", ""
	}
	if auth.Attributes != nil {
		apiKey = strings.TrimSpace(auth.Attributes["api_key"])
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
	}
	if apiKey == "" && auth.Metadata != nil {
		if v, ok := auth.Metadata["access_token"].(string); ok {
			apiKey = strings.TrimSpace(v)
		}
	}
	return apiKey, baseURL
}

// codexCredsForLiveModels extracts the Codex/OpenAI API key and optional base URL
// for live model loading.
func codexCredsForLiveModels(auth *coreauth.Auth) (apiKey, baseURL string) {
	if auth == nil {
		return "", ""
	}
	if auth.Attributes != nil {
		apiKey = strings.TrimSpace(auth.Attributes["api_key"])
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
	}
	if apiKey == "" && auth.Metadata != nil {
		if v, ok := auth.Metadata["access_token"].(string); ok {
			apiKey = strings.TrimSpace(v)
		}
	}
	return apiKey, baseURL
}

// geminiAPIKeyForLiveModels extracts the Gemini/AIStudio API key for live model
// loading.
func geminiAPIKeyForLiveModels(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["api_key"]); v != "" {
			return v
		}
	}
	return ""
}

// resolveGeminiBaseURLForLiveModels returns the configured Gemini base URL or the
// default Generative Language API origin.
func resolveGeminiBaseURLForLiveModels(auth *coreauth.Auth) string {
	const defaultBase = "https://generativelanguage.googleapis.com"
	if auth == nil || auth.Attributes == nil {
		return defaultBase
	}
	if custom := strings.TrimSpace(auth.Attributes["base_url"]); custom != "" {
		return strings.TrimRight(custom, "/")
	}
	return defaultBase
}

// resolveAIStudioBaseURLForLiveModels returns the configured AI Studio base URL
// or the default Generative Language API origin.
func resolveAIStudioBaseURLForLiveModels(auth *coreauth.Auth) string {
	return resolveGeminiBaseURLForLiveModels(auth)
}

// vertexCredsForLiveModels extracts the Vertex API key and optional base URL for
// live model loading. Service-account auth does not expose an API key, so live
// loading only works for API-key Vertex credentials.
func vertexCredsForLiveModels(auth *coreauth.Auth) (apiKey, baseURL string) {
	if auth == nil {
		return "", ""
	}
	if auth.Attributes != nil {
		apiKey = strings.TrimSpace(auth.Attributes["api_key"])
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
	}
	return apiKey, baseURL
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
