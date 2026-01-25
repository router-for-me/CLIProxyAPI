package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

// ProviderInfo represents information about a configured provider
type ProviderInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Label    string `json:"label,omitempty"`
	Prefix   string `json:"prefix,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
	ProxyURL string `json:"proxy_url,omitempty"`
	APIKey   string `json:"api_key,omitempty"` // masked
	Status   string `json:"status"`
	Disabled bool   `json:"disabled"`
}

// ProviderHealth represents the health status of a provider
type ProviderHealth struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Label       string `json:"label,omitempty"`
	BaseURL     string `json:"base_url,omitempty"`
	Status      string `json:"status"` // "healthy", "unhealthy"
	Message     string `json:"message,omitempty"`
	Latency     int64  `json:"latency_ms,omitempty"`
	ModelTested string `json:"model_tested,omitempty"`
}

// ListProviders returns all configured API key providers from configuration
func (h *Handler) ListProviders(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	// Filter by type if specified
	typeFilter := strings.ToLower(strings.TrimSpace(c.Query("type")))

	auths := h.authManager.List()
	providers := make([]ProviderInfo, 0)

	for _, auth := range auths {
		// Only include API key providers (those with api_key attribute)
		if !isAPIKeyProvider(auth) {
			continue
		}

		providerType := getProviderType(auth)
		if typeFilter != "" && !strings.EqualFold(providerType, typeFilter) {
			continue
		}

		info := ProviderInfo{
			ID:       auth.ID,
			Name:     auth.Provider,
			Type:     providerType,
			Label:    auth.Label,
			Prefix:   auth.Prefix,
			BaseURL:  authAttribute(auth, "base_url"),
			ProxyURL: auth.ProxyURL,
			APIKey:   util.HideAPIKey(authAttribute(auth, "api_key")),
			Status:   string(auth.Status),
			Disabled: auth.Disabled,
		}
		providers = append(providers, info)
	}

	c.JSON(http.StatusOK, gin.H{
		"total":     len(providers),
		"providers": providers,
	})
}

// CheckProvidersHealth performs health checks on configured API key providers
func (h *Handler) CheckProvidersHealth(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	// Parse query parameters
	nameFilter := strings.TrimSpace(c.Query("name"))
	typeFilter := strings.ToLower(strings.TrimSpace(c.Query("type")))
	modelFilter := strings.TrimSpace(c.Query("model"))
	modelsFilter := strings.TrimSpace(c.Query("models"))
	isConcurrent := c.DefaultQuery("concurrent", "false") == "true"
	timeoutSeconds := 15
	if ts := c.Query("timeout"); ts != "" {
		if parsed, err := strconv.Atoi(ts); err == nil && parsed >= 5 && parsed <= 60 {
			timeoutSeconds = parsed
		}
	}

	// Build model filter set
	var modelFilterSet map[string]struct{}
	if modelFilter != "" || modelsFilter != "" {
		modelFilterSet = make(map[string]struct{})
		if modelFilter != "" {
			modelFilterSet[strings.ToLower(modelFilter)] = struct{}{}
		}
		if modelsFilter != "" {
			for _, m := range strings.Split(modelsFilter, ",") {
				trimmed := strings.TrimSpace(m)
				if trimmed != "" {
					modelFilterSet[strings.ToLower(trimmed)] = struct{}{}
				}
			}
		}
	}

	// Get all API key providers
	auths := h.authManager.List()
	targetAuths := make([]*coreauth.Auth, 0)

	for _, auth := range auths {
		if !isAPIKeyProvider(auth) {
			continue
		}
		if auth.Disabled {
			continue
		}

		// Apply name filter
		if nameFilter != "" {
			if !strings.EqualFold(auth.ID, nameFilter) &&
				!strings.EqualFold(auth.Provider, nameFilter) &&
				!strings.EqualFold(auth.Label, nameFilter) {
				continue
			}
		}

		// Apply type filter
		providerType := getProviderType(auth)
		if typeFilter != "" && !strings.EqualFold(providerType, typeFilter) {
			continue
		}

		targetAuths = append(targetAuths, auth)
	}

	if len(targetAuths) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"status":          "healthy",
			"healthy_count":   0,
			"unhealthy_count": 0,
			"total_count":     0,
			"providers":       []ProviderHealth{},
		})
		return
	}

	// Prepare health check results
	results := make([]ProviderHealth, 0, len(targetAuths))
	var wg sync.WaitGroup
	var mu sync.Mutex

	checkProvider := func(auth *coreauth.Auth) {
		defer wg.Done()

		providerType := getProviderType(auth)
		baseURL := authAttribute(auth, "base_url")

		// Get models for this provider
		reg := registry.GetGlobalRegistry()
		models := reg.GetModelsForClient(auth.ID)

		// Apply model filter if specified
		if len(modelFilterSet) > 0 {
			filtered := make([]*registry.ModelInfo, 0)
			for _, model := range models {
				if _, ok := modelFilterSet[strings.ToLower(model.ID)]; ok {
					filtered = append(filtered, model)
				}
			}
			models = filtered
		}

		// If no models available, report as unhealthy
		if len(models) == 0 {
			mu.Lock()
			results = append(results, ProviderHealth{
				ID:      auth.ID,
				Name:    auth.Provider,
				Type:    providerType,
				Label:   auth.Label,
				BaseURL: baseURL,
				Status:  "unhealthy",
				Message: "no models available for this provider",
			})
			mu.Unlock()
			return
		}

		// Use the first model for health check
		testModel := models[0]

		startTime := time.Now()
		checkCtx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)
		defer cancel()

		// Build minimal OpenAI-format request for health check
		openAIRequest := map[string]interface{}{
			"model": testModel.ID,
			"messages": []map[string]interface{}{
				{"role": "user", "content": "hi"},
				{"role": "system", "content": "test"},
			},
			"stream":     true,
			"max_tokens": 1,
		}

		requestJSON, err := json.Marshal(openAIRequest)
		if err != nil {
			mu.Lock()
			results = append(results, ProviderHealth{
				ID:          auth.ID,
				Name:        auth.Provider,
				Type:        providerType,
				Label:       auth.Label,
				BaseURL:     baseURL,
				Status:      "unhealthy",
				Message:     fmt.Sprintf("failed to build request: %v", err),
				ModelTested: testModel.ID,
			})
			mu.Unlock()
			return
		}

		// Build executor request
		req := cliproxyexecutor.Request{
			Model:   testModel.ID,
			Payload: requestJSON,
			Format:  sdktranslator.FormatOpenAI,
		}

		opts := cliproxyexecutor.Options{
			Stream:          true,
			SourceFormat:    sdktranslator.FormatOpenAI,
			OriginalRequest: requestJSON,
		}

		// Execute stream directly with the specific auth
		stream, err := h.authManager.ExecuteStreamWithAuth(checkCtx, auth, req, opts)
		if err != nil {
			mu.Lock()
			results = append(results, ProviderHealth{
				ID:          auth.ID,
				Name:        auth.Provider,
				Type:        providerType,
				Label:       auth.Label,
				BaseURL:     baseURL,
				Status:      "unhealthy",
				Message:     err.Error(),
				ModelTested: testModel.ID,
			})
			mu.Unlock()
			return
		}

		// Wait for first chunk or timeout
		select {
		case chunk, ok := <-stream:
			if ok {
				if chunk.Err != nil {
					mu.Lock()
					results = append(results, ProviderHealth{
						ID:          auth.ID,
						Name:        auth.Provider,
						Type:        providerType,
						Label:       auth.Label,
						BaseURL:     baseURL,
						Status:      "unhealthy",
						Message:     chunk.Err.Error(),
						ModelTested: testModel.ID,
					})
					mu.Unlock()
					cancel()
					go func() {
						for range stream {
						}
					}()
					return
				}

				// Got first chunk - provider is healthy
				latency := time.Since(startTime).Milliseconds()
				cancel()
				go func() {
					for range stream {
					}
				}()

				mu.Lock()
				results = append(results, ProviderHealth{
					ID:          auth.ID,
					Name:        auth.Provider,
					Type:        providerType,
					Label:       auth.Label,
					BaseURL:     baseURL,
					Status:      "healthy",
					Latency:     latency,
					ModelTested: testModel.ID,
				})
				mu.Unlock()
			} else {
				mu.Lock()
				results = append(results, ProviderHealth{
					ID:          auth.ID,
					Name:        auth.Provider,
					Type:        providerType,
					Label:       auth.Label,
					BaseURL:     baseURL,
					Status:      "unhealthy",
					Message:     "stream closed without data",
					ModelTested: testModel.ID,
				})
				mu.Unlock()
			}
		case <-checkCtx.Done():
			mu.Lock()
			results = append(results, ProviderHealth{
				ID:          auth.ID,
				Name:        auth.Provider,
				Type:        providerType,
				Label:       auth.Label,
				BaseURL:     baseURL,
				Status:      "unhealthy",
				Message:     "health check timeout",
				ModelTested: testModel.ID,
			})
			mu.Unlock()
		}
	}

	// Execute health checks
	if isConcurrent {
		for _, auth := range targetAuths {
			wg.Add(1)
			go checkProvider(auth)
		}
	} else {
		for _, auth := range targetAuths {
			wg.Add(1)
			checkProvider(auth)
		}
	}

	wg.Wait()

	// Count results
	healthyCount := 0
	unhealthyCount := 0
	for _, result := range results {
		if result.Status == "healthy" {
			healthyCount++
		} else {
			unhealthyCount++
		}
	}

	// Determine overall status
	overallStatus := "healthy"
	if unhealthyCount > 0 && healthyCount == 0 {
		overallStatus = "unhealthy"
	} else if unhealthyCount > 0 {
		overallStatus = "partial"
	}

	c.JSON(http.StatusOK, gin.H{
		"status":          overallStatus,
		"healthy_count":   healthyCount,
		"unhealthy_count": unhealthyCount,
		"total_count":     len(results),
		"providers":       results,
	})
}

// isAPIKeyProvider checks if an auth entry is an API key provider (from config)
func isAPIKeyProvider(auth *coreauth.Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	// Check for api_key attribute or label containing "apikey"
	if _, ok := auth.Attributes["api_key"]; ok {
		return true
	}
	if strings.Contains(strings.ToLower(auth.Label), "apikey") {
		return true
	}
	// Check source attribute for config-based providers
	source := strings.ToLower(auth.Attributes["source"])
	return strings.HasPrefix(source, "config:")
}

// getProviderType returns the type of provider (gemini, claude, codex, openai-compatibility, vertex)
func getProviderType(auth *coreauth.Auth) string {
	if auth == nil {
		return "unknown"
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	switch provider {
	case "gemini":
		return "gemini-api-key"
	case "claude":
		return "claude-api-key"
	case "codex":
		return "codex-api-key"
	case "vertex":
		return "vertex-api-key"
	default:
		// Check if it's openai-compatibility
		if auth.Attributes != nil {
			if _, ok := auth.Attributes["compat_name"]; ok {
				return "openai-compatibility"
			}
		}
		return provider
	}
}
