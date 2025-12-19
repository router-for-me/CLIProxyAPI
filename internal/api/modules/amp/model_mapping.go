// Package amp provides model mapping functionality for routing Amp CLI requests
// to alternative models when the requested model is not available locally.
package amp

import (
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

// ModelMapper provides model name mapping/aliasing for Amp CLI requests.
// When an Amp request comes in for a model that isn't available locally,
// this mapper can redirect it to an alternative model that IS available.
type ModelMapper interface {
	// MapModel returns the target model name if a mapping exists and the target
	// model has available providers. Returns empty string if no mapping applies.
	MapModel(requestedModel string) string

	// UpdateMappings refreshes the mapping configuration (for hot-reload).
	UpdateMappings(mappings []config.AmpModelMapping)

	// GetFallbacks returns the list of fallback models for a requested model.
	GetFallbacks(requestedModel string) []string
}

// DefaultModelMapper implements ModelMapper with thread-safe mapping storage.
type DefaultModelMapper struct {
	mu       sync.RWMutex
	mappings map[string][]string // from -> [to1, to2, ...] (normalized lowercase keys)
}

// NewModelMapper creates a new model mapper with the given initial mappings.
func NewModelMapper(mappings []config.AmpModelMapping) *DefaultModelMapper {
	m := &DefaultModelMapper{
		mappings: make(map[string][]string),
	}
	m.UpdateMappings(mappings)
	return m
}

// MapModel checks if a mapping exists for the requested model and returns
// the first target model that has available local providers.
// It supports recursive resolution (alias -> alias -> target) and detects cycles.
// Returns empty string if no valid mapping exists or no targets have providers.
func (m *DefaultModelMapper) MapModel(requestedModel string) string {
	if requestedModel == "" {
		return ""
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.resolveModel(requestedModel, nil)
}

// resolveModel performs the recursive resolution of model mappings.
// visited keeps track of visited models in the recursion stack to detect cycles.
func (m *DefaultModelMapper) resolveModel(model string, visited map[string]struct{}) string {
	normalizedRequest := strings.ToLower(strings.TrimSpace(model))

	// Cycle detection
	if visited == nil {
		visited = make(map[string]struct{})
	}
	if _, ok := visited[normalizedRequest]; ok {
		log.Warnf("amp model mapping: detected cycle for model %q, stopping recursion", model)
		return ""
	}
	visited[normalizedRequest] = struct{}{}
	defer delete(visited, normalizedRequest)

	// Check for mapping
	targets, exists := m.mappings[normalizedRequest]
	if !exists || len(targets) == 0 {
		// No mapping found.
		return ""
	}

	// Iterate through targets (fallbacks)
	for _, target := range targets {
		// 1. Check if this target is a real provider
		providers := util.GetProviderName(target)
		if len(providers) > 0 {
			log.Debugf("amp model mapping: resolved %s -> %s (provider found)", model, target)
			return target
		}

		// 2. If no provider, try to resolve recursively (if this target is also an alias)
		// We only continue recursion if this target is DEFINED in our mappings.
		normalizedTarget := strings.ToLower(strings.TrimSpace(target))
		if _, isAlias := m.mappings[normalizedTarget]; isAlias {
			resolved := m.resolveModel(target, visited)
			if resolved != "" {
				log.Debugf("amp model mapping: recursive resolution %s -> %s -> %s", model, target, resolved)
				return resolved
			}
		} else {
			log.Debugf("amp model mapping: target model %s has no available providers and is not an alias", target)
		}
	}

	return ""
}

// GetFallbacks returns the configured fallback list for a model, specifically for debugging or info.
func (m *DefaultModelMapper) GetFallbacks(requestedModel string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	normalizedRequest := strings.ToLower(strings.TrimSpace(requestedModel))
	if targets, exists := m.mappings[normalizedRequest]; exists {
		// Return a copy
		copied := make([]string, len(targets))
		copy(copied, targets)
		return copied
	}
	return nil
}

// UpdateMappings refreshes the mapping configuration from config.
// This is called during initialization and on config hot-reload.
func (m *DefaultModelMapper) UpdateMappings(mappings []config.AmpModelMapping) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Clear and rebuild mappings
	m.mappings = make(map[string][]string, len(mappings))

	for _, mapping := range mappings {
		from := strings.TrimSpace(mapping.From)
		if from == "" {
			log.Warnf("amp model mapping: skipping mapping with empty 'from' field")
			continue
		}

		// Handle StringOrSlice for 'To' field
		var targets []string
		for _, t := range mapping.To {
			t = strings.TrimSpace(t)
			if t != "" {
				targets = append(targets, t)
			}
		}

		if len(targets) == 0 {
			log.Warnf("amp model mapping: skipping mapping for %q with no valid 'to' targets", from)
			continue
		}

		// Store with normalized lowercase key for case-insensitive lookup
		normalizedFrom := strings.ToLower(from)
		m.mappings[normalizedFrom] = targets

		if len(targets) == 1 {
			log.Debugf("amp model mapping registered: %s -> %s", from, targets[0])
		} else {
			log.Debugf("amp model mapping registered: %s -> %v (fallback chain)", from, targets)
		}
	}

	if len(m.mappings) > 0 {
		log.Infof("amp model mapping: loaded %d mapping(s)", len(m.mappings))
	}
}

// GetMappings returns a copy of current mappings (for debugging/status).
func (m *DefaultModelMapper) GetMappings() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]string, len(m.mappings))
	for k, v := range m.mappings {
		if len(v) > 0 {
			result[k] = v[0] // Return primary mapping for backward compatibility
		}
	}
	return result
}
