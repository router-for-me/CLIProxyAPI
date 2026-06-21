package cliproxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

const (
	antigravityModelBaseURLDaily = "https://daily-cloudcode-pa.googleapis.com"
	antigravityModelBaseURLProd  = "https://cloudcode-pa.googleapis.com"
	antigravityModelsPath        = "/v1internal:fetchAvailableModels"
)

type antigravityFetchAvailableModelsResponse struct {
	Models            map[string]antigravityFetchedModel `json:"models"`
	WebSearchModelIDs []string                           `json:"webSearchModelIds"`
}

type antigravityFetchedModel struct {
	DisplayName         string `json:"displayName"`
	MaxTokens           int    `json:"maxTokens"`
	MaxCompletionTokens int    `json:"maxOutputTokens"`
}

type antigravityModelCapabilityHints struct {
	WebSearchModelIDs map[string]struct{}
}

type antigravityFetchedModels struct {
	Models []*ModelInfo
	Hints  antigravityModelCapabilityHints
}

func (s *Service) fetchAntigravityModelsForAuth(ctx context.Context, auth *coreauth.Auth) antigravityFetchedModels {
	if auth == nil || auth.Metadata == nil {
		return antigravityFetchedModels{}
	}
	accessToken, _ := auth.Metadata["access_token"].(string)
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return antigravityFetchedModels{}
	}

	client := &http.Client{}
	if transport, _, errProxy := proxyutil.BuildHTTPTransport(s.antigravityModelFetchProxyURL(auth)); errProxy == nil && transport != nil {
		client.Transport = transport
	}

	for _, baseURL := range antigravityModelBaseURLs(auth) {
		req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+antigravityModelsPath, strings.NewReader(antigravityModelFetchPayload(auth)))
		if errReq != nil {
			continue
		}
		req.Close = true
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("User-Agent", misc.AntigravityUserAgent())

		resp, errDo := client.Do(req)
		if errDo != nil {
			continue
		}
		body, errRead := io.ReadAll(resp.Body)
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("antigravity model fetch: close response body: %v", errClose)
		}
		if errRead != nil {
			continue
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			continue
		}
		fetched := parseAntigravityFetchedModels(body)
		if len(fetched.Models) > 0 || len(fetched.Hints.WebSearchModelIDs) > 0 {
			return fetched
		}
	}
	return antigravityFetchedModels{}
}

func (s *Service) antigravityModelFetchProxyURL(auth *coreauth.Auth) string {
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
	}
	if s != nil && s.cfg != nil {
		return strings.TrimSpace(s.cfg.ProxyURL)
	}
	return ""
}

func antigravityModelBaseURLs(auth *coreauth.Auth) []string {
	if baseURL := resolveAntigravityModelBaseURL(auth); baseURL != "" {
		return []string{baseURL}
	}
	return []string{antigravityModelBaseURLDaily, antigravityModelBaseURLProd}
}

func resolveAntigravityModelBaseURL(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if value := strings.TrimSpace(auth.Attributes["base_url"]); value != "" {
			return strings.TrimRight(value, "/")
		}
	}
	if auth.Metadata != nil {
		if value, ok := auth.Metadata["base_url"].(string); ok {
			value = strings.TrimSpace(value)
			if value != "" {
				return strings.TrimRight(value, "/")
			}
		}
	}
	return ""
}

func antigravityModelFetchPayload(auth *coreauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return `{}`
	}
	projectID, _ := auth.Metadata["project_id"].(string)
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return `{}`
	}
	body, err := json.Marshal(map[string]string{"project": projectID})
	if err != nil {
		return `{}`
	}
	return string(body)
}

func parseAntigravityFetchedModels(body []byte) antigravityFetchedModels {
	var parsed antigravityFetchAvailableModelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return antigravityFetchedModels{}
	}

	models := make([]*ModelInfo, 0, len(parsed.Models))
	modelIDs := make([]string, 0, len(parsed.Models))
	for modelID := range parsed.Models {
		modelID = strings.TrimSpace(modelID)
		if modelID != "" {
			modelIDs = append(modelIDs, modelID)
		}
	}
	sort.Strings(modelIDs)
	for _, modelID := range modelIDs {
		modelData := parsed.Models[modelID]
		displayName := strings.TrimSpace(modelData.DisplayName)
		if displayName == "" {
			displayName = modelID
		}
		model := &ModelInfo{
			ID:                  modelID,
			Object:              "model",
			OwnedBy:             "antigravity",
			Type:                "antigravity",
			DisplayName:         displayName,
			Name:                modelID,
			Description:         displayName,
			ContextLength:       modelData.MaxTokens,
			MaxCompletionTokens: modelData.MaxCompletionTokens,
		}
		models = append(models, model)
	}

	return antigravityFetchedModels{
		Models: models,
		Hints:  parseAntigravityModelCapabilityHintsFromResponse(parsed),
	}
}

func parseAntigravityModelCapabilityHintsFromResponse(parsed antigravityFetchAvailableModelsResponse) antigravityModelCapabilityHints {
	webSearchModels := make(map[string]struct{}, len(parsed.WebSearchModelIDs))
	for _, modelID := range parsed.WebSearchModelIDs {
		modelID = normalizeAntigravityFetchedModelID(modelID)
		if modelID != "" {
			webSearchModels[modelID] = struct{}{}
		}
	}
	return antigravityModelCapabilityHints{WebSearchModelIDs: webSearchModels}
}

func applyAntigravityFetchedModelCapabilities(models []*ModelInfo, hints antigravityModelCapabilityHints) []*ModelInfo {
	if len(models) == 0 || len(hints.WebSearchModelIDs) == 0 {
		return models
	}

	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := normalizeAntigravityFetchedModelID(model.ID)
		if _, ok := hints.WebSearchModelIDs[modelID]; ok {
			model.SupportsWebSearch = true
		}
	}
	return models
}

func applyAntigravityFetchedModels(staticModels []*ModelInfo, fetched antigravityFetchedModels) []*ModelInfo {
	if len(fetched.Models) == 0 {
		return applyAntigravityFetchedModelCapabilities(staticModels, fetched.Hints)
	}

	staticByID := make(map[string]*ModelInfo, len(staticModels))
	staticByDisplayName := make(map[string]*ModelInfo, len(staticModels))
	staticByDisplayFamily := make(map[string]*ModelInfo, len(staticModels))
	for _, model := range staticModels {
		if model == nil {
			continue
		}
		id := normalizeAntigravityFetchedModelID(model.ID)
		if id != "" {
			staticByID[id] = model
		}
		if displayName := normalizeAntigravityDisplayName(model.DisplayName); displayName != "" {
			if _, exists := staticByDisplayName[displayName]; !exists {
				staticByDisplayName[displayName] = model
			}
		}
		if family := normalizeAntigravityDisplayFamily(model.DisplayName); family != "" {
			if _, exists := staticByDisplayFamily[family]; !exists {
				staticByDisplayFamily[family] = model
			}
		}
	}

	models := make([]*ModelInfo, 0, len(fetched.Models))
	for _, model := range fetched.Models {
		if model == nil {
			continue
		}
		merged := *model
		staticModel := staticByID[normalizeAntigravityFetchedModelID(model.ID)]
		if staticModel == nil {
			staticModel = staticByDisplayName[normalizeAntigravityDisplayName(model.DisplayName)]
		}
		if staticModel == nil {
			staticModel = staticByDisplayFamily[normalizeAntigravityDisplayFamily(model.DisplayName)]
		}
		if staticModel != nil {
			mergeAntigravityStaticModelMetadata(&merged, staticModel)
		}
		models = append(models, &merged)
	}
	return applyAntigravityFetchedModelCapabilities(models, fetched.Hints)
}

func mergeAntigravityStaticModelMetadata(model, staticModel *ModelInfo) {
	if model == nil || staticModel == nil {
		return
	}
	if model.Object == "" {
		model.Object = staticModel.Object
	}
	if model.OwnedBy == "" {
		model.OwnedBy = staticModel.OwnedBy
	}
	if model.Type == "" {
		model.Type = staticModel.Type
	}
	if model.ContextLength == 0 && staticModel.ContextLength > 0 {
		model.ContextLength = staticModel.ContextLength
	}
	if model.MaxCompletionTokens == 0 && staticModel.MaxCompletionTokens > 0 {
		model.MaxCompletionTokens = staticModel.MaxCompletionTokens
	}
	if model.Thinking == nil {
		model.Thinking = cloneThinkingSupport(staticModel.Thinking)
	}
	if len(model.SupportedParameters) == 0 {
		model.SupportedParameters = append([]string(nil), staticModel.SupportedParameters...)
	}
	if len(model.SupportedInputModalities) == 0 {
		model.SupportedInputModalities = append([]string(nil), staticModel.SupportedInputModalities...)
	}
	if len(model.SupportedOutputModalities) == 0 {
		model.SupportedOutputModalities = append([]string(nil), staticModel.SupportedOutputModalities...)
	}
}

func cloneThinkingSupport(thinking *registry.ThinkingSupport) *registry.ThinkingSupport {
	if thinking == nil {
		return nil
	}
	cloned := *thinking
	if len(thinking.Levels) > 0 {
		cloned.Levels = append([]string(nil), thinking.Levels...)
	}
	return &cloned
}

func normalizeAntigravityDisplayName(displayName string) string {
	return strings.ToLower(strings.TrimSpace(displayName))
}

func normalizeAntigravityDisplayFamily(displayName string) string {
	displayName = normalizeAntigravityDisplayName(displayName)
	if displayName == "" {
		return ""
	}
	if idx := strings.Index(displayName, "("); idx >= 0 {
		displayName = strings.TrimSpace(displayName[:idx])
	}
	return displayName
}

func normalizeAntigravityFetchedModelID(modelID string) string {
	return strings.ToLower(strings.TrimSpace(modelID))
}
