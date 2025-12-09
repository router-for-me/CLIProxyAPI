// Package amp provides model mapping functionality for routing Amp CLI requests
// to alternative models when the requested model is not available locally.
package amp

import (
	"sort"
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
}

// DefaultModelMapper implements ModelMapper with thread-safe mapping storage.
type DefaultModelMapper struct {
	mu       sync.RWMutex
	mappings map[string]string // from -> to (normalized lowercase keys)
}

// NewModelMapper creates a new model mapper with the given initial mappings.
func NewModelMapper(mappings []config.AmpModelMapping) *DefaultModelMapper {
	m := &DefaultModelMapper{
		mappings: make(map[string]string),
	}
	m.UpdateMappings(mappings)
	return m
}

// MapModel checks if a mapping exists for the requested model and if the
// target model has available local providers. Returns the mapped model name
// or empty string if no valid mapping exists.
func (m *DefaultModelMapper) MapModel(requestedModel string) string {
	if requestedModel == "" {
		return ""
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	// Normalize the requested model for lookup
	// Replace underscores with dashes for consistent lookup (e.g., claude-sonnet-4_5 -> claude-sonnet-4-5)
	normalizedRequest := strings.ToLower(strings.TrimSpace(requestedModel))
	normalizedRequest = strings.ReplaceAll(normalizedRequest, "_", "-")

	// Check for direct mapping first
	targetModel, exists := m.mappings[normalizedRequest]

	// If no direct match, try prefix/wildcard matching with deterministic order
	// This allows mappings like "claude-haiku-*" to match "claude-haiku-4-5-20251001"
	if !exists {
		patterns := make([]string, 0, len(m.mappings))
		for pattern := range m.mappings {
			if strings.HasSuffix(pattern, "*") {
				patterns = append(patterns, pattern)
			}
		}
		sort.Slice(patterns, func(i, j int) bool {
			if len(patterns[i]) == len(patterns[j]) {
				return patterns[i] < patterns[j]
			}
			return len(patterns[i]) > len(patterns[j])
		})
		for _, pattern := range patterns {
			prefix := strings.TrimSuffix(pattern, "*")
			if strings.HasPrefix(normalizedRequest, prefix) {
				targetModel = m.mappings[pattern]
				exists = true
				log.Debugf("amp model mapping: wildcard match %s -> %s (pattern: %s)", normalizedRequest, targetModel, pattern)
				break
			}
		}
	}

	if !exists {
		return ""
	}

	// Verify target model has available providers
	providers := util.GetProviderName(targetModel)
	if len(providers) == 0 {
		log.Debugf("amp model mapping: target model %s has no available providers, skipping mapping", targetModel)
		return ""
	}

	// Note: Detailed routing log is handled by logAmpRouting in fallback_handlers.go
	return targetModel
}

// UpdateMappings refreshes the mapping configuration from config.
// This is called during initialization and on config hot-reload.
func (m *DefaultModelMapper) UpdateMappings(mappings []config.AmpModelMapping) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Clear and rebuild mappings
	m.mappings = make(map[string]string, len(mappings))

	for _, mapping := range mappings {
		from := strings.TrimSpace(mapping.From)
		to := strings.TrimSpace(mapping.To)

		if from == "" || to == "" {
			log.Warnf("amp model mapping: skipping invalid mapping (from=%q, to=%q)", from, to)
			continue
		}

		// Store with normalized lowercase key for case-insensitive lookup
		// Also normalize underscores to dashes for consistent matching
		normalizedFrom := strings.ToLower(from)
		normalizedFrom = strings.ReplaceAll(normalizedFrom, "_", "-")
		m.mappings[normalizedFrom] = to

		log.Debugf("amp model mapping registered: %s -> %s", from, to)
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
		result[k] = v
	}
	return result
}
