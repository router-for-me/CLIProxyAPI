// Package registry provides OpenRouter context length enrichment for model metadata.
package registry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	openRouterFetchTimeout   = 30 * time.Second
	openRouterRefreshInterval = 24 * time.Hour
	openRouterModelsURL      = "https://openrouter.ai/api/v1/models"
)

var enrichmentOnce sync.Once

// openRouterModel represents a model in OpenRouter's API response
type openRouterModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextLength int    `json:"context_length"`
}

// openRouterModelsResponse is the response from OpenRouter's models endpoint
type openRouterModelsResponse struct {
	Data []openRouterModel `json:"data"`
}

// openRouterEnrichmentStore tracks enrichment state
type openRouterEnrichmentStore struct {
	mu            sync.RWMutex
	lastRefresh   time.Time
	contextLength map[string]int // model ID -> context length
}

var openRouterStore = &openRouterEnrichmentStore{
	contextLength: make(map[string]int),
}

// StartOpenRouterEnrichment starts a background goroutine that fetches
// context_length metadata from OpenRouter's public models endpoint.
// Runs immediately on startup and then refreshes every 24 hours.
func StartOpenRouterEnrichment(ctx context.Context) {
	enrichmentOnce.Do(func() {
		go runOpenRouterEnrichment(ctx)
	})
}

func runOpenRouterEnrichment(ctx context.Context) {
	// Initial fetch
	fetchAndEnrichOpenRouter(ctx)

	// Periodic refresh
	ticker := time.NewTicker(openRouterRefreshInterval)
	defer ticker.Stop()
	log.Infof("OpenRouter enrichment started (interval=%s)", openRouterRefreshInterval)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fetchAndEnrichOpenRouter(ctx)
		}
	}
}

// fetchAndEnrichOpenRouter fetches models from OpenRouter and enriches
// registered models that lack context_length metadata.
// Returns the number of models actually enriched.
func fetchAndEnrichOpenRouter(ctx context.Context) int {
	client := &http.Client{Timeout: openRouterFetchTimeout}
	reqCtx, cancel := context.WithTimeout(ctx, openRouterFetchTimeout)
	req, err := http.NewRequestWithContext(reqCtx, "GET", openRouterModelsURL, nil)
	if err != nil {
		cancel()
		log.Debugf("OpenRouter enrichment: request creation failed: %v", err)
		return 0
	}

	resp, err := client.Do(req)
	if err != nil {
		cancel()
		log.Debugf("OpenRouter enrichment: fetch failed: %v", err)
		return 0
	}

	if resp.StatusCode != 200 {
		resp.Body.Close()
		cancel()
		log.Debugf("OpenRouter enrichment: returned status %d", resp.StatusCode)
		return 0
	}

	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	cancel()

	if err != nil {
		log.Debugf("OpenRouter enrichment: read error: %v", err)
		return 0
	}

	var parsed openRouterModelsResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		log.Warnf("OpenRouter enrichment: parse failed: %v", err)
		return 0
	}

	return enrichModelsFromOpenRouter(parsed.Data)
}

// enrichModelsFromOpenRouter updates the global registry with context_length
// values from OpenRouter for models that lack this metadata.
// Matches by exact model ID or by checking if the local ID is contained in
// or contains the OpenRouter ID (e.g., "gemini-3.1-pro" matches "google/gemini-3.1-pro-preview").
// Returns the number of models actually enriched.
func enrichModelsFromOpenRouter(models []openRouterModel) int {
	enrichedCount := 0
	registry := GetGlobalRegistry()

	// Build a map of model ID to context length from OpenRouter
	openRouterContextLengths := make(map[string]int, len(models))
	for _, m := range models {
		if m.ContextLength > 0 {
			openRouterContextLengths[m.ID] = m.ContextLength
		}
	}

	// Get all registered models and enrich those lacking context_length
	allModels := registry.GetAvailableModels("openai")
	for _, modelMap := range allModels {
		modelID, _ := modelMap["id"].(string)
		if modelID == "" {
			continue
		}

		// Skip if already has context_length
		if _, hasCL := modelMap["context_length"]; hasCL {
			continue
		}

		// Try to find a matching OpenRouter model ID
		var ctxLen int
		var found bool

		// First try exact match
		if cl, ok := openRouterContextLengths[modelID]; ok {
			ctxLen = cl
			found = true
		} else {
			// Try substring matching:
			// - Check if local ID is contained in OpenRouter ID (e.g., "gemini-3.1-pro" in "google/gemini-3.1-pro-preview")
			// - Check if OpenRouter ID is contained in local ID
			// - Check if OpenRouter ID suffix matches local ID
			for orID, cl := range openRouterContextLengths {
				if strings.Contains(orID, modelID) || strings.Contains(modelID, orID) {
					// Extract the base name from OpenRouter ID (after last slash)
					orBase := orID
					if slashIdx := strings.LastIndex(orID, "/"); slashIdx >= 0 {
						orBase = orID[slashIdx+1:]
					}
					// Check if local ID matches the base or is a prefix/suffix
					if orBase == modelID || strings.HasPrefix(orBase, modelID) || strings.HasPrefix(modelID, orBase) {
						ctxLen = cl
						found = true
						break
					}
				}
			}
		}

		if found {
			// Update the live model registration (not a clone)
			if registry.SetModelContextLength(modelID, ctxLen) {
				enrichedCount++
				log.Debugf("enriched model %s with context_length=%d from openrouter", modelID, ctxLen)
			}
		}
	}

	// Update store
	openRouterStore.mu.Lock()
	for modelID, ctxLen := range openRouterContextLengths {
		openRouterStore.contextLength[modelID] = ctxLen
	}
	openRouterStore.lastRefresh = time.Now()
	openRouterStore.mu.Unlock()

	if enrichedCount > 0 {
		log.Infof("OpenRouter enrichment: enriched %d models with context_length", enrichedCount)
	}

	return enrichedCount
}

// GetOpenRouterContextLength returns the cached context_length for a model from OpenRouter.
// Returns 0 if not found.
func GetOpenRouterContextLength(modelID string) int {
	openRouterStore.mu.RLock()
	defer openRouterStore.mu.RUnlock()
	return openRouterStore.contextLength[modelID]
}

// TriggerOpenRouterRefresh forces an immediate refresh of OpenRouter context enrichment.
// Returns the number of models actually enriched (not the cache size).
func TriggerOpenRouterRefresh(ctx context.Context) int {
	return fetchAndEnrichOpenRouter(ctx)
}

// GetOpenRouterLastRefresh returns the last refresh time.
func GetOpenRouterLastRefresh() time.Time {
	openRouterStore.mu.RLock()
	defer openRouterStore.mu.RUnlock()
	return openRouterStore.lastRefresh
}

// GetOpenRouterContextLengthSource returns "openrouter" if the context_length
// for a model came from OpenRouter enrichment, empty string otherwise.
func GetOpenRouterContextLengthSource(modelID string) string {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return ""
	}
	openRouterStore.mu.RLock()
	defer openRouterStore.mu.RUnlock()
	if _, ok := openRouterStore.contextLength[modelID]; ok {
		return "openrouter"
	}
	return ""
}

// GetOpenRouterEnrichedModels returns all model IDs that have been enriched
// by OpenRouter with their context_length values.
func GetOpenRouterEnrichedModels() map[string]int {
	openRouterStore.mu.RLock()
	defer openRouterStore.mu.RUnlock()
	result := make(map[string]int, len(openRouterStore.contextLength))
	for k, v := range openRouterStore.contextLength {
		result[k] = v
	}
	return result
}

// BuildModelSources constructs a sources map indicating where each model's
// context_length originated (static, openrouter, provider, etc.).
func BuildModelSources(registry *ModelRegistry) map[string]string {
	if registry == nil {
		return nil
	}

	sources := make(map[string]string)
	registry.mutex.RLock()
	defer registry.mutex.RUnlock()

	for modelID, registration := range registry.models {
		if registration == nil || registration.Info == nil {
			continue
		}

		// Determine source based on model ID prefix and enrichment state
		switch {
		case strings.HasPrefix(modelID, "claude-"):
			if GetOpenRouterContextLengthSource(modelID) != "" {
				sources[modelID] = "openrouter"
			} else {
				sources[modelID] = "static"
			}
		case strings.HasPrefix(modelID, "gemini-"), strings.HasPrefix(modelID, "models/gemini-"):
			sources[modelID] = "static"
		case strings.HasPrefix(modelID, "gpt-"), strings.HasPrefix(modelID, "chatgpt-"), strings.HasPrefix(modelID, "o1-"):
			if GetOpenRouterContextLengthSource(modelID) != "" {
				sources[modelID] = "openrouter"
			} else {
				sources[modelID] = "provider"
			}
		default:
			// For OpenRouter-hosted models, check enrichment
			if GetOpenRouterContextLengthSource(modelID) != "" {
				sources[modelID] = "openrouter"
			} else {
				sources[modelID] = "provider"
			}
		}
	}

	return sources
}
