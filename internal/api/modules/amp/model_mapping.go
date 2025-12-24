// Package amp provides model mapping functionality for routing Amp CLI requests
// to alternative models when the requested model is not available locally.
package amp

import (
	"regexp"
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
	mappings map[string]string // exact: from -> to (normalized lowercase keys)
	regexps  []regexMapping    // regex rules evaluated in order
}

// NewModelMapper creates a new model mapper with the given initial mappings.
func NewModelMapper(mappings []config.AmpModelMapping) *DefaultModelMapper {
	m := &DefaultModelMapper{
		mappings: make(map[string]string),
		regexps:  nil,
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
	normalizedRequest := strings.ToLower(strings.TrimSpace(requestedModel))

	// Check for direct mapping
	targetModel, exists := m.mappings[normalizedRequest]
	if !exists {
		// Try regex mappings in order
		base, _ := util.NormalizeThinkingModel(requestedModel)
		for _, rm := range m.regexps {
			if rm.re.MatchString(requestedModel) || (base != "" && rm.re.MatchString(base)) {
				targetModel = rm.to
				exists = true
				break
			}
		}
		if !exists {
			return ""
		}
	}

	// Verify target model has available providers
	normalizedTarget, _ := util.NormalizeThinkingModel(targetModel)
	providers := util.GetProviderName(normalizedTarget)
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
	m.regexps = make([]regexMapping, 0, len(mappings))

	for _, mapping := range mappings {
		from := strings.TrimSpace(mapping.From)
		to := strings.TrimSpace(mapping.To)

		if from == "" || to == "" {
			log.Warnf("amp model mapping: skipping invalid mapping (from=%q, to=%q)", from, to)
			continue
		}

		if mapping.Regex {
			// Compile case-insensitive regex; wrap with (?i) to match behavior of exact lookups
			pattern := "(?i)" + from
			re, err := regexp.Compile(pattern)
			if err != nil {
				log.Warnf("amp model mapping: invalid regex %q: %v", from, err)
				continue
			}
			m.regexps = append(m.regexps, regexMapping{re: re, to: to})
			log.Debugf("amp model regex mapping registered: /%s/ -> %s", from, to)
		} else {
			// Store with normalized lowercase key for case-insensitive lookup
			normalizedFrom := strings.ToLower(from)
			m.mappings[normalizedFrom] = to
			log.Debugf("amp model mapping registered: %s -> %s", from, to)
		}
	}

	if len(m.mappings) > 0 {
		log.Infof("amp model mapping: loaded %d mapping(s)", len(m.mappings))
	}
	if n := len(m.regexps); n > 0 {
		log.Infof("amp model mapping: loaded %d regex mapping(s)", n)
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

type regexMapping struct {
	re *regexp.Regexp
	to string
}
