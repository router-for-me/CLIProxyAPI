// Package routing provides intelligent model routing with fallback candidates.
// It allows mapping virtual model names to multiple actual models, trying each
// in priority order until one succeeds, with support for fuzzy matching.
package routing

import (
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

// ModelRouter handles intelligent model routing with fallback candidates.
type ModelRouter struct {
	mu     sync.RWMutex
	cfg    *config.ModelRoutingConfig
	cache  *RouteCache
	routes map[string]*config.ModelRouteEntry // exact match index
	fuzzy  []*config.ModelRouteEntry          // fuzzy match entries
}

// NewModelRouter creates a new ModelRouter with the given configuration.
func NewModelRouter(cfg *config.ModelRoutingConfig, authDir string) *ModelRouter {
	r := &ModelRouter{
		routes: make(map[string]*config.ModelRouteEntry),
		fuzzy:  make([]*config.ModelRouteEntry, 0),
	}
	if cfg != nil {
		r.UpdateConfig(cfg, authDir)
	}
	return r
}

// UpdateConfig updates the router configuration and rebuilds internal indexes.
func (r *ModelRouter) UpdateConfig(cfg *config.ModelRoutingConfig, authDir string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.cfg = cfg
	r.routes = make(map[string]*config.ModelRouteEntry)
	r.fuzzy = make([]*config.ModelRouteEntry, 0)

	if cfg == nil || !cfg.Enabled {
		return
	}

	// Build indexes for fast lookup
	for i := range cfg.Routes {
		entry := &cfg.Routes[i]
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			continue
		}

		if entry.Fuzzy {
			r.fuzzy = append(r.fuzzy, entry)
		} else {
			// Exact match: index by lowercase name
			r.routes[strings.ToLower(name)] = entry
		}
	}

	// Initialize or update cache
	cacheFile := cfg.CacheFile
	if cacheFile == "" {
		cacheFile = "model-routing-cache.json"
	}
	if r.cache == nil {
		r.cache = NewRouteCache(authDir, cacheFile)
	} else {
		r.cache.UpdatePath(authDir, cacheFile)
	}

	log.Debugf("model router: loaded %d exact routes, %d fuzzy routes", len(r.routes), len(r.fuzzy))
}

// IsEnabled returns whether the model routing feature is enabled.
func (r *ModelRouter) IsEnabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg != nil && r.cfg.Enabled
}

// GetCandidates returns the list of candidate models for the given model name.
// It first checks for cached successful routes, then looks up routing rules,
// and finally performs automatic fuzzy search across all providers if enabled.
// Returns nil if no routing rule matches and auto-search finds nothing.
func (r *ModelRouter) GetCandidates(modelName string) []string {
	if !r.IsEnabled() {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return nil
	}

	// Check cache first - if we have a previously successful model, prioritize it
	if r.cache != nil {
		if cached := r.cache.Get(modelName); cached != "" {
			// Return cached model as first candidate, followed by other candidates
			entry := r.findRouteEntry(modelName)
			if entry != nil {
				candidates := make([]string, 0, len(entry.Candidates)+1)
				candidates = append(candidates, cached)
				for _, c := range entry.Candidates {
					if c != cached {
						candidates = append(candidates, c)
					}
				}
				return candidates
			}
			// Also include auto-search results after cached model
			autoResults := r.autoSearchModels(modelName)
			if len(autoResults) > 0 {
				candidates := make([]string, 0, len(autoResults)+1)
				candidates = append(candidates, cached)
				for _, m := range autoResults {
					if m != cached {
						candidates = append(candidates, m)
					}
				}
				return candidates
			}
			return []string{cached}
		}
	}

	// Find matching route entry from config
	entry := r.findRouteEntry(modelName)
	if entry != nil {
		return entry.Candidates
	}

	// No explicit route configured - try automatic fuzzy search
	if r.cfg != nil && r.cfg.AutoSearch {
		return r.autoSearchModels(modelName)
	}

	return nil
}

// autoSearchModels searches the model registry for models containing the given name.
// It extracts the base model name (removing version suffixes like -20251124) and
// searches for all models containing that base name across all providers.
func (r *ModelRouter) autoSearchModels(modelName string) []string {
	// Extract base model name by removing common version suffixes
	baseName := extractBaseModelName(modelName)
	if baseName == "" {
		return nil
	}

	lowerBase := strings.ToLower(baseName)
	reg := registry.GetGlobalRegistry()
	allModels := reg.GetAvailableModels("")

	var candidates []string
	seen := make(map[string]struct{})

	for _, model := range allModels {
		modelID, ok := model["id"].(string)
		if !ok || modelID == "" {
			continue
		}

		// Check if model ID contains the base name (case-insensitive)
		if strings.Contains(strings.ToLower(modelID), lowerBase) {
			// Verify the model has providers
			if len(util.GetProviderName(modelID)) > 0 {
				if _, exists := seen[modelID]; !exists {
					seen[modelID] = struct{}{}
					candidates = append(candidates, modelID)
				}
			}
		}
	}

	if len(candidates) > 0 {
		log.Debugf("model router: auto-search for '%s' (base: '%s') found %d candidates: %v",
			modelName, baseName, len(candidates), candidates)
	}

	return candidates
}

// extractBaseModelName extracts the base model name by removing version suffixes.
// For example: "Claude-sonnet-20251124" -> "Claude-sonnet"
//
//	"gpt-4-turbo-2024-04-09" -> "gpt-4-turbo"
func extractBaseModelName(modelName string) string {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return ""
	}

	// Common patterns for version suffixes:
	// - Date format: -20251124, -2024-04-09
	// - Version numbers at end: -v1, -v2.0
	// We'll remove trailing date-like patterns

	parts := strings.Split(modelName, "-")
	if len(parts) <= 1 {
		return modelName
	}

	// Find where the version suffix starts
	cutIndex := len(parts)
	for i := len(parts) - 1; i >= 0; i-- {
		part := parts[i]
		// Check if this part looks like a date (8 digits) or version number
		if isDateLikeSuffix(part) || isVersionSuffix(part) {
			cutIndex = i
		} else {
			break
		}
	}

	if cutIndex == 0 {
		return modelName // Don't remove everything
	}

	return strings.Join(parts[:cutIndex], "-")
}

// isDateLikeSuffix checks if a string looks like a date suffix (e.g., "20251124", "2024")
func isDateLikeSuffix(s string) bool {
	if len(s) < 4 {
		return false
	}
	// Check if all characters are digits
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	// 4 digits (year) or 8 digits (YYYYMMDD) or 6 digits (YYYYMM)
	return len(s) == 4 || len(s) == 8 || len(s) == 6
}

// isVersionSuffix checks if a string looks like a version suffix (e.g., "v1", "v2.0", "latest")
func isVersionSuffix(s string) bool {
	s = strings.ToLower(s)
	if s == "latest" || s == "preview" || s == "beta" || s == "alpha" {
		return true
	}
	// Check for v1, v2, v1.0, etc.
	if len(s) >= 2 && s[0] == 'v' {
		for _, c := range s[1:] {
			if c != '.' && (c < '0' || c > '9') {
				return false
			}
		}
		return true
	}
	return false
}

// findRouteEntry finds the matching route entry for the given model name.
// Must be called with r.mu held (at least RLock).
func (r *ModelRouter) findRouteEntry(modelName string) *config.ModelRouteEntry {
	lowerName := strings.ToLower(modelName)

	// Try exact match first
	if entry, ok := r.routes[lowerName]; ok {
		return entry
	}

	// Try fuzzy matches
	for _, entry := range r.fuzzy {
		pattern := strings.ToLower(entry.Name)
		if strings.Contains(lowerName, pattern) {
			return entry
		}
	}

	return nil
}

// ResolveFuzzyCandidate resolves a candidate pattern to an actual available model.
// If the candidate contains wildcards (*), it searches the model registry.
// Otherwise, it checks if the model has available providers.
func (r *ModelRouter) ResolveFuzzyCandidate(candidate string) string {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return ""
	}

	// If no wildcard, check directly if model has providers
	if !strings.Contains(candidate, "*") {
		if len(util.GetProviderName(candidate)) > 0 {
			return candidate
		}
		return ""
	}

	// Wildcard pattern: search registry for matching models
	reg := registry.GetGlobalRegistry()
	allModels := reg.GetAvailableModels("")

	for _, model := range allModels {
		modelID, ok := model["id"].(string)
		if !ok || modelID == "" {
			continue
		}
		if matchWildcard(candidate, modelID) {
			// Verify the matched model has providers
			if len(util.GetProviderName(modelID)) > 0 {
				return modelID
			}
		}
	}

	return ""
}

// RecordSuccess records a successful model route for future use.
func (r *ModelRouter) RecordSuccess(virtualModel, actualModel string) {
	if !r.IsEnabled() {
		return
	}

	r.mu.RLock()
	cache := r.cache
	r.mu.RUnlock()

	if cache != nil {
		cache.Set(virtualModel, actualModel)
	}

	log.Debugf("model router: recorded success %s -> %s", virtualModel, actualModel)
}

// matchWildcard checks if the text matches the wildcard pattern.
// Supports * as a wildcard that matches any sequence of characters.
// The matching is case-insensitive.
func matchWildcard(pattern, text string) bool {
	pattern = strings.ToLower(pattern)
	text = strings.ToLower(text)

	// Handle simple cases
	if pattern == "*" {
		return true
	}
	if pattern == "" {
		return text == ""
	}
	if !strings.Contains(pattern, "*") {
		return pattern == text
	}

	// Split pattern by * and match parts
	parts := strings.Split(pattern, "*")
	if len(parts) == 0 {
		return true
	}

	// Check prefix (before first *)
	if parts[0] != "" && !strings.HasPrefix(text, parts[0]) {
		return false
	}
	text = text[len(parts[0]):]

	// Check suffix (after last *)
	lastPart := parts[len(parts)-1]
	if lastPart != "" && !strings.HasSuffix(text, lastPart) {
		return false
	}
	if lastPart != "" {
		text = text[:len(text)-len(lastPart)]
	}

	// Check middle parts
	for i := 1; i < len(parts)-1; i++ {
		part := parts[i]
		if part == "" {
			continue
		}
		idx := strings.Index(text, part)
		if idx < 0 {
			return false
		}
		text = text[idx+len(part):]
	}

	return true
}
