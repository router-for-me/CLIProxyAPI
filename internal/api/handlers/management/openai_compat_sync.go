package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

const (
	openAICompatSyncDefaultTimeout = 15 * time.Second
)

var modelScopeOpenAPIBaseURL = "https://modelscope.cn/openapi/v1"

type openAICompatSyncRequest struct {
	Name          string `json:"name"`
	All           bool   `json:"all"`
	TimeoutSecond *int   `json:"timeout_seconds"`
}

type openAICompatSyncResult struct {
	Provider      string
	FetchedCount  int
	UpdatedCount  int
	Unmatched     []string
	Warnings      []string
	UpdatedModels []config.OpenAICompatibilityModel
}

type openAICompatListModelsResponse struct {
	Data   []openAICompatListModel `json:"data"`
	Models []openAICompatListModel `json:"models"`
}

type openAICompatListModel struct {
	ID string `json:"id"`
}

type modelScopeListResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Models []modelScopeModel `json:"models"`
	} `json:"data"`
	Message string `json:"message"`
}

type modelScopeModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Downloads   int64  `json:"downloads"`
}

// SyncOpenAICompatModels fetches latest models for OpenAI-compatible providers and rewrites
// the provider model list using ModelScope display_name as canonical alias when available.
func (h *Handler) SyncOpenAICompatModels(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "errors": []string{"config unavailable"}})
		return
	}
	if strings.TrimSpace(h.configFilePath) == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "errors": []string{"config path not configured"}})
		return
	}

	var req openAICompatSyncRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "errors": []string{"invalid body"}})
		return
	}

	timeout := openAICompatSyncDefaultTimeout
	if req.TimeoutSecond != nil {
		if *req.TimeoutSecond <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "errors": []string{"timeout_seconds must be greater than 0"}})
			return
		}
		timeout = time.Duration(*req.TimeoutSecond) * time.Second
	}

	selected, errSelect := selectOpenAICompatProviders(h.cfg.OpenAICompatibility, strings.TrimSpace(req.Name), req.All)
	if errSelect != nil {
		status := http.StatusBadRequest
		if errSelect == errOpenAICompatProviderNotFound {
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"status": "error", "errors": []string{errSelect.Error()}})
		return
	}

	ctx := c.Request.Context()
	results := make([]openAICompatSyncResult, 0, len(selected))
	totalFetched := 0
	totalUpdated := 0
	unmatchedByProvider := make(map[string][]string)
	warnings := make([]string, 0)

	for _, idx := range selected {
		entry := h.cfg.OpenAICompatibility[idx]
		result, errSync := h.syncSingleOpenAICompatProvider(ctx, entry, timeout)
		if errSync != nil {
			errMsg := fmt.Sprintf("provider %s sync failed: %v", providerDisplayName(entry), errSync)
			c.JSON(http.StatusBadGateway, gin.H{
				"status":           "error",
				"providers":        collectProviderNames(results),
				"updated_count":    totalUpdated,
				"fetched_count":    totalFetched,
				"unmatched_models": unmatchedByProvider,
				"errors":           append(warnings, errMsg),
			})
			return
		}
		results = append(results, result)
		totalFetched += result.FetchedCount
		totalUpdated += result.UpdatedCount
		if len(result.Unmatched) > 0 {
			unmatchedByProvider[result.Provider] = append([]string(nil), result.Unmatched...)
		}
		if len(result.Warnings) > 0 {
			warnings = append(warnings, result.Warnings...)
		}
	}

	updatedProviders := cloneOpenAICompatibilityEntries(h.cfg.OpenAICompatibility)
	for _, idx := range selected {
		for i := range results {
			if strings.EqualFold(updatedProviders[idx].Name, results[i].Provider) {
				updatedProviders[idx].Models = append([]config.OpenAICompatibilityModel(nil), results[i].UpdatedModels...)
				break
			}
		}
	}

	originalProviders := h.cfg.OpenAICompatibility
	h.cfg.OpenAICompatibility = updatedProviders
	h.cfg.SanitizeOpenAICompatibility()
	if errPersist := h.persistConfigOnly(); errPersist != nil {
		h.cfg.OpenAICompatibility = originalProviders
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":           "error",
			"providers":        collectProviderNames(results),
			"updated_count":    totalUpdated,
			"fetched_count":    totalFetched,
			"unmatched_models": unmatchedByProvider,
			"errors":           append(warnings, fmt.Sprintf("failed to save config: %v", errPersist)),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":           "ok",
		"providers":        collectProviderNames(results),
		"updated_count":    totalUpdated,
		"fetched_count":    totalFetched,
		"unmatched_models": unmatchedByProvider,
		"errors":           warnings,
	})
}

var errOpenAICompatProviderNotFound = fmt.Errorf("openai-compatibility provider not found")

func selectOpenAICompatProviders(entries []config.OpenAICompatibility, name string, all bool) ([]int, error) {
	if len(entries) == 0 {
		return nil, errOpenAICompatProviderNotFound
	}
	if all {
		out := make([]int, 0, len(entries))
		for i := range entries {
			out = append(out, i)
		}
		return out, nil
	}
	if name == "" {
		return nil, fmt.Errorf("name is required when all is false")
	}
	out := make([]int, 0, 1)
	for i := range entries {
		if strings.EqualFold(strings.TrimSpace(entries[i].Name), name) {
			out = append(out, i)
		}
	}
	if len(out) == 0 {
		return nil, errOpenAICompatProviderNotFound
	}
	return out, nil
}

func (h *Handler) syncSingleOpenAICompatProvider(ctx context.Context, entry config.OpenAICompatibility, timeout time.Duration) (openAICompatSyncResult, error) {
	providerName := providerDisplayName(entry)
	modelNames, warnings, errFetch := h.fetchOpenAICompatUpstreamModels(ctx, entry, timeout)
	if errFetch != nil {
		return openAICompatSyncResult{}, errFetch
	}

	models := make([]config.OpenAICompatibilityModel, 0, len(modelNames))
	unmatched := make([]string, 0)
	cache := make(map[string]struct {
		Alias string
		Match bool
	}, len(modelNames))

	for _, name := range modelNames {
		cached, ok := cache[name]
		if !ok {
			alias, matched, errLookup := h.lookupModelScopeCanonicalAlias(ctx, name, timeout)
			if errLookup != nil {
				return openAICompatSyncResult{}, errLookup
			}
			cached = struct {
				Alias string
				Match bool
			}{
				Alias: alias,
				Match: matched,
			}
			cache[name] = cached
		}

		model := config.OpenAICompatibilityModel{Name: name}
		if cached.Match {
			alias := strings.TrimSpace(cached.Alias)
			if alias != "" && alias != strings.TrimSpace(name) {
				model.Alias = alias
			}
		} else {
			unmatched = append(unmatched, name)
		}
		models = append(models, model)
	}

	return openAICompatSyncResult{
		Provider:      providerName,
		FetchedCount:  len(modelNames),
		UpdatedCount:  len(models),
		Unmatched:     unmatched,
		Warnings:      warnings,
		UpdatedModels: models,
	}, nil
}

func (h *Handler) fetchOpenAICompatUpstreamModels(ctx context.Context, entry config.OpenAICompatibility, timeout time.Duration) ([]string, []string, error) {
	baseURL := strings.TrimSpace(entry.BaseURL)
	if baseURL == "" {
		return nil, nil, fmt.Errorf("provider %s base-url is empty", providerDisplayName(entry))
	}

	credentials := entry.APIKeyEntries
	if len(credentials) == 0 {
		credentials = []config.OpenAICompatibilityAPIKey{{}}
	}

	seen := make(map[string]string)
	warnings := make([]string, 0)

	for idx := range credentials {
		cred := credentials[idx]
		models, errFetch := h.fetchModelsForSingleCredential(ctx, baseURL, cred, entry.Headers, timeout)
		if errFetch != nil {
			warnings = append(warnings, fmt.Sprintf("provider %s credential[%d]: %v", providerDisplayName(entry), idx, errFetch))
			continue
		}
		for _, modelID := range models {
			key := strings.ToLower(strings.TrimSpace(modelID))
			if key == "" {
				continue
			}
			if _, exists := seen[key]; !exists {
				seen[key] = modelID
			}
		}
	}

	if len(seen) == 0 {
		if len(warnings) > 0 {
			return nil, warnings, fmt.Errorf("failed to fetch models from all credentials")
		}
		return nil, warnings, fmt.Errorf("upstream returned empty model list")
	}

	out := make([]string, 0, len(seen))
	for _, modelID := range seen {
		out = append(out, modelID)
	}
	sort.Slice(out, func(i, j int) bool {
		a := strings.ToLower(out[i])
		b := strings.ToLower(out[j])
		if a == b {
			return out[i] < out[j]
		}
		return a < b
	})
	return out, warnings, nil
}

func (h *Handler) fetchModelsForSingleCredential(ctx context.Context, baseURL string, cred config.OpenAICompatibilityAPIKey, headers map[string]string, timeout time.Duration) ([]string, error) {
	requestURL, errURL := joinURLPath(baseURL, "models")
	if errURL != nil {
		return nil, errURL
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, errReq := http.NewRequestWithContext(callCtx, http.MethodGet, requestURL, nil)
	if errReq != nil {
		return nil, errReq
	}
	req.Header.Set("Accept", "application/json")
	if apiKey := strings.TrimSpace(cred.APIKey); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	for k, v := range headers {
		key := strings.TrimSpace(k)
		value := strings.TrimSpace(v)
		if key == "" || value == "" {
			continue
		}
		req.Header.Set(key, value)
	}

	client := &http.Client{
		Timeout:   timeout,
		Transport: h.apiCallTransport(&coreauth.Auth{ProxyURL: strings.TrimSpace(cred.ProxyURL)}),
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, errDo
	}
	defer func() { _ = resp.Body.Close() }()

	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if errRead != nil {
		return nil, errRead
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	modelIDs, errDecode := decodeOpenAICompatModelIDs(body)
	if errDecode != nil {
		return nil, errDecode
	}
	if len(modelIDs) == 0 {
		return nil, fmt.Errorf("empty models response")
	}
	return modelIDs, nil
}

func decodeOpenAICompatModelIDs(body []byte) ([]string, error) {
	var payload openAICompatListModelsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(payload.Data)+len(payload.Models))
	seen := make(map[string]struct{})
	appendID := func(raw string) {
		id := strings.TrimSpace(raw)
		if id == "" {
			return
		}
		key := strings.ToLower(id)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		out = append(out, id)
	}
	for _, item := range payload.Data {
		appendID(item.ID)
	}
	for _, item := range payload.Models {
		appendID(item.ID)
	}
	return out, nil
}

func (h *Handler) lookupModelScopeCanonicalAlias(ctx context.Context, upstreamModel string, timeout time.Duration) (string, bool, error) {
	upstreamModel = strings.TrimSpace(upstreamModel)
	if upstreamModel == "" {
		return "", false, nil
	}

	base, errBase := url.Parse(strings.TrimSpace(modelScopeOpenAPIBaseURL))
	if errBase != nil {
		return "", false, errBase
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/models"
	query := base.Query()
	query.Set("search", upstreamModel)
	query.Set("page_number", "1")
	query.Set("page_size", "20")
	query.Set("sort", "downloads")
	base.RawQuery = query.Encode()

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, errReq := http.NewRequestWithContext(callCtx, http.MethodGet, base.String(), nil)
	if errReq != nil {
		return "", false, errReq
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{
		Timeout:   timeout,
		Transport: h.apiCallTransport(nil),
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return "", false, errDo
	}
	defer func() { _ = resp.Body.Close() }()

	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if errRead != nil {
		return "", false, errRead
	}
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("modelscope status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload modelScopeListResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", false, err
	}
	if !payload.Success {
		msg := strings.TrimSpace(payload.Message)
		if msg == "" {
			msg = "modelscope response success=false"
		}
		return "", false, fmt.Errorf("%s", msg)
	}

	target := normalizeModelComparable(upstreamModel)
	if target == "" {
		return "", false, nil
	}

	var best *modelScopeModel
	for i := range payload.Data.Models {
		item := &payload.Data.Models[i]
		displayName := strings.TrimSpace(item.DisplayName)
		id := strings.TrimSpace(item.ID)
		if normalizeModelComparable(displayName) != target && normalizeModelComparable(id) != target {
			continue
		}
		if best == nil || item.Downloads > best.Downloads || (item.Downloads == best.Downloads && strings.Compare(displayName, strings.TrimSpace(best.DisplayName)) < 0) {
			best = item
		}
	}
	if best == nil {
		return "", false, nil
	}

	alias := strings.TrimSpace(best.DisplayName)
	if alias == "" {
		return "", false, nil
	}
	return alias, true, nil
}

func normalizeModelComparable(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}
	var builder strings.Builder
	builder.Grow(len(raw))
	for _, r := range raw {
		switch r {
		case '-', '_', '.':
			continue
		default:
			if unicode.IsSpace(r) {
				continue
			}
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func providerDisplayName(entry config.OpenAICompatibility) string {
	name := strings.TrimSpace(entry.Name)
	if name != "" {
		return name
	}
	return "openai-compatibility"
}

func collectProviderNames(results []openAICompatSyncResult) []string {
	names := make([]string, 0, len(results))
	for _, result := range results {
		names = append(names, result.Provider)
	}
	return names
}

func cloneOpenAICompatibilityEntries(entries []config.OpenAICompatibility) []config.OpenAICompatibility {
	if len(entries) == 0 {
		return nil
	}
	out := make([]config.OpenAICompatibility, len(entries))
	for i := range entries {
		entry := entries[i]
		if len(entry.APIKeyEntries) > 0 {
			entry.APIKeyEntries = append([]config.OpenAICompatibilityAPIKey(nil), entry.APIKeyEntries...)
		}
		if len(entry.Models) > 0 {
			entry.Models = append([]config.OpenAICompatibilityModel(nil), entry.Models...)
		}
		if len(entry.Headers) > 0 {
			headers := make(map[string]string, len(entry.Headers))
			for key, value := range entry.Headers {
				headers[key] = value
			}
			entry.Headers = headers
		}
		out[i] = entry
	}
	return out
}

func (h *Handler) persistConfigOnly() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return config.SaveConfigPreserveComments(h.configFilePath, h.cfg)
}

func joinURLPath(baseURL string, pathSuffix string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", err
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + strings.TrimLeft(strings.TrimSpace(pathSuffix), "/")
	return parsed.String(), nil
}
