package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	modelCacheTTL          = 5 * time.Minute
	modelDiscoveryPageSize = 50
	modelDiscoveryMaxPages = 10
)

type kiroModelListResponse struct {
	Models    []kiroAvailableModel `json:"models"`
	NextToken string               `json:"nextToken"`
}

type kiroAvailableModel struct {
	ModelID             string   `json:"modelId"`
	ModelName           string   `json:"modelName"`
	Description         string   `json:"description"`
	SupportedInputTypes []string `json:"supportedInputTypes"`
	TokenLimits         struct {
		MaxInputTokens  int `json:"maxInputTokens"`
		MaxOutputTokens int `json:"maxOutputTokens"`
	} `json:"tokenLimits"`
}

type modelCacheEntry struct {
	Models    []pluginapi.ModelInfo
	FetchedAt time.Time
}

type rpcHostLogRequest struct {
	HostCallbackID string         `json:"host_callback_id,omitempty"`
	Level          string         `json:"level,omitempty"`
	Message        string         `json:"message,omitempty"`
	Fields         map[string]any `json:"fields,omitempty"`
}

var (
	modelCacheMu sync.Mutex
	modelCache   = make(map[string]modelCacheEntry)
)

func modelsForAuth(raw []byte) ([]byte, error) {
	var request rpcAuthModelRequest
	if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
		return nil, fmt.Errorf("decode Kiro model discovery request: %w", errUnmarshal)
	}

	models, errDiscovery := accountModels(request)
	if errDiscovery != nil {
		logModelDiscoveryFallback(request, errDiscovery)
	}
	return okEnvelope(pluginapi.ModelResponse{Provider: pluginIdentifier, Models: models})
}

func accountModels(request rpcAuthModelRequest) ([]pluginapi.ModelInfo, error) {
	credential, handled, errCredential := decodeCredential(request.StorageJSON)
	if errCredential != nil {
		return configuredModels(), fmt.Errorf("decode credential: %w", errCredential)
	}
	if !handled {
		return configuredModels(), fmt.Errorf("selected auth is not a Kiro credential")
	}
	key, errKey := resolveAPIKey(credential)
	if errKey != nil {
		return configuredModels(), fmt.Errorf("resolve API key: %w", errKey)
	}

	cacheKey := modelCacheKey(request.AuthID, credential.Region, key)
	if models, okCache := cachedAccountModels(cacheKey, true); okCache {
		return filterDiscoveredModels(models), nil
	}

	models, errDiscovery := fetchAvailableModels(request.HostCallbackID, credential, key)
	if errDiscovery != nil {
		if stale, okStale := cachedAccountModels(cacheKey, false); okStale {
			return filterDiscoveredModels(stale), fmt.Errorf("refresh models; using stale cache: %w", errDiscovery)
		}
		return configuredModels(), fmt.Errorf("discover models; using configured fallback: %w", errDiscovery)
	}
	storeAccountModels(cacheKey, models)
	return filterDiscoveredModels(models), nil
}

func fetchAvailableModels(hostCallbackID string, credential kiroCredential, key string) ([]pluginapi.ModelInfo, error) {
	models := make([]pluginapi.ModelInfo, 0)
	seenModels := make(map[string]struct{})
	seenTokens := make(map[string]struct{})
	nextToken := ""

	for page := 0; page < modelDiscoveryMaxPages; page++ {
		endpoint, errEndpoint := modelDiscoveryURL(credential.Region, nextToken)
		if errEndpoint != nil {
			return nil, errEndpoint
		}
		rawResponse, errCall := invokeHost(pluginabi.MethodHostHTTPDo, rpcHostHTTPRequest{
			HostCallbackID: hostCallbackID,
			Method:         http.MethodGet,
			URL:            endpoint,
			Headers: http.Header{
				"Accept":                      []string{"application/json"},
				"Authorization":               []string{"Bearer " + key},
				"Tokentype":                   []string{"API_KEY"},
				"X-Amzn-Codewhisperer-Optout": []string{"false"},
			},
		})
		if errCall != nil {
			return nil, fmt.Errorf("call model-list endpoint: %w", errCall)
		}
		var response pluginapi.HTTPResponse
		if errUnmarshal := json.Unmarshal(rawResponse, &response); errUnmarshal != nil {
			return nil, fmt.Errorf("decode host HTTP response: %w", errUnmarshal)
		}
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("model-list endpoint returned HTTP %d: %s", response.StatusCode, limitedErrorBody(response.Body))
		}

		var pageResponse kiroModelListResponse
		if errUnmarshal := json.Unmarshal(response.Body, &pageResponse); errUnmarshal != nil {
			return nil, fmt.Errorf("decode model-list response: %w", errUnmarshal)
		}
		for _, model := range pageResponse.Models {
			modelID := strings.TrimSpace(model.ModelID)
			if modelID == "" {
				continue
			}
			keyModel := strings.ToLower(modelID)
			if _, exists := seenModels[keyModel]; exists {
				continue
			}
			seenModels[keyModel] = struct{}{}
			models = append(models, discoveredModelInfo(model))
		}

		nextToken = strings.TrimSpace(pageResponse.NextToken)
		if nextToken == "" {
			return models, nil
		}
		if _, repeated := seenTokens[nextToken]; repeated {
			return nil, fmt.Errorf("model-list pagination repeated a next token")
		}
		seenTokens[nextToken] = struct{}{}
	}

	return models, nil
}

func modelDiscoveryURL(region, nextToken string) (string, error) {
	if !regionPattern.MatchString(region) {
		return "", fmt.Errorf("invalid Kiro region %q", region)
	}
	query := url.Values{
		"origin":     []string{"AI_EDITOR"},
		"maxResults": []string{fmt.Sprintf("%d", modelDiscoveryPageSize)},
	}
	if nextToken != "" {
		query.Set("nextToken", nextToken)
	}
	return "https://codewhisperer." + region + ".amazonaws.com/ListAvailableModels?" + query.Encode(), nil
}

func discoveredModelInfo(model kiroAvailableModel) pluginapi.ModelInfo {
	inputLimit := int64(model.TokenLimits.MaxInputTokens)
	if inputLimit <= 0 {
		inputLimit = 200000
	}
	outputLimit := int64(model.TokenLimits.MaxOutputTokens)
	if outputLimit <= 0 {
		outputLimit = 64000
	}
	displayName := firstNonEmpty(model.ModelName, model.ModelID)
	return pluginapi.ModelInfo{
		ID:                         strings.TrimSpace(model.ModelID),
		Name:                       displayName,
		DisplayName:                displayName,
		Object:                     "model",
		OwnedBy:                    "kiro",
		Type:                       "claude",
		Description:                strings.TrimSpace(model.Description),
		ContextLength:              inputLimit,
		InputTokenLimit:            inputLimit,
		OutputTokenLimit:           outputLimit,
		MaxCompletionTokens:        outputLimit,
		SupportedGenerationMethods: []string{"generateContent", "streamGenerateContent"},
		SupportedParameters:        []string{"max_tokens", "temperature", "top_p", "tools"},
		SupportedInputModalities:   discoveredInputModalities(model.SupportedInputTypes),
		SupportedOutputModalities:  []string{"text"},
	}
}

func discoveredInputModalities(inputTypes []string) []string {
	modalities := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	for _, inputType := range inputTypes {
		var modality string
		switch strings.ToUpper(strings.TrimSpace(inputType)) {
		case "TEXT":
			modality = "text"
		case "IMAGE":
			modality = "image"
		default:
			continue
		}
		if _, exists := seen[modality]; exists {
			continue
		}
		seen[modality] = struct{}{}
		modalities = append(modalities, modality)
	}
	if len(modalities) == 0 {
		return []string{"text"}
	}
	return modalities
}

func filterDiscoveredModels(models []pluginapi.ModelInfo) []pluginapi.ModelInfo {
	configured := loadedConfig().Models
	allowed := make(map[string]struct{}, len(configured))
	allowAll := false
	for _, model := range configured {
		model = strings.ToLower(strings.TrimSpace(model))
		if model == "*" {
			allowAll = true
			break
		}
		if model != "" {
			allowed[model] = struct{}{}
		}
	}

	out := make([]pluginapi.ModelInfo, 0, len(models))
	for _, model := range models {
		if !allowAll {
			if _, okAllowed := allowed[strings.ToLower(strings.TrimSpace(model.ID))]; !okAllowed {
				continue
			}
		}
		out = append(out, model)
	}
	return out
}

func modelCacheKey(authID, region, key string) string {
	sum := sha256.Sum256([]byte(key))
	return strings.TrimSpace(authID) + "\x00" + region + "\x00" + hex.EncodeToString(sum[:8])
}

func cachedAccountModels(cacheKey string, freshOnly bool) ([]pluginapi.ModelInfo, bool) {
	modelCacheMu.Lock()
	defer modelCacheMu.Unlock()
	entry, okCache := modelCache[cacheKey]
	if !okCache || freshOnly && time.Since(entry.FetchedAt) >= modelCacheTTL {
		return nil, false
	}
	return append([]pluginapi.ModelInfo(nil), entry.Models...), true
}

func storeAccountModels(cacheKey string, models []pluginapi.ModelInfo) {
	modelCacheMu.Lock()
	modelCache[cacheKey] = modelCacheEntry{Models: append([]pluginapi.ModelInfo(nil), models...), FetchedAt: time.Now()}
	modelCacheMu.Unlock()
}

func resetModelCache() {
	modelCacheMu.Lock()
	modelCache = make(map[string]modelCacheEntry)
	modelCacheMu.Unlock()
}

func logModelDiscoveryFallback(request rpcAuthModelRequest, errDiscovery error) {
	_, _ = invokeHost(pluginabi.MethodHostLog, rpcHostLogRequest{
		HostCallbackID: request.HostCallbackID,
		Level:          "warn",
		Message:        "Kiro model discovery failed; using cached or configured models",
		Fields: map[string]any{
			"auth_id": request.AuthID,
			"error":   errDiscovery.Error(),
		},
	})
}

func limitedErrorBody(body []byte) string {
	const limit = 1024
	body = []byte(strings.TrimSpace(string(body)))
	if len(body) > limit {
		body = body[:limit]
	}
	return string(body)
}
