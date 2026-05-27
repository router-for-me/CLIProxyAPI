// Package amp provides model mapping functionality for routing Amp CLI requests
// to alternative models when the requested model is not available locally.
package amp

import (
	"regexp"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

// ModelMapper provides model name mapping/aliasing for Amp CLI requests.
// When an Amp request comes in for a model that isn't available locally,
// this mapper can redirect it to an alternative model that IS available.
type ModelMapper interface {
	// MapModel returns the target model name if a mapping exists and the target
	// model has available providers. Returns empty string if no mapping applies.
	MapModel(requestedModel string) string

	// MapModelWithInfo returns the target model plus the matching mapping metadata.
	MapModelWithInfo(requestedModel string) MappingResult

	// UpdateMappings refreshes the mapping configuration (for hot-reload).
	UpdateMappings(mappings []config.AmpModelMapping)
}

// MappingResult is the result of resolving an Amp model mapping.
type MappingResult struct {
	Model   string
	Mapping config.AmpModelMapping
}

// DefaultModelMapper implements ModelMapper with thread-safe mapping storage.
type DefaultModelMapper struct {
	mu       sync.RWMutex
	mappings map[string]config.AmpModelMapping // exact: from -> mapping (normalized lowercase keys)
	regexps  []regexMapping                    // regex rules evaluated in order
}

// NewModelMapper creates a new model mapper with the given initial mappings.
func NewModelMapper(mappings []config.AmpModelMapping) *DefaultModelMapper {
	m := &DefaultModelMapper{
		mappings: make(map[string]config.AmpModelMapping),
		regexps:  nil,
	}
	m.UpdateMappings(mappings)
	return m
}

// MapModel checks if a mapping exists for the requested model and if the
// target model has available local providers. Returns the mapped model name
// or empty string if no valid mapping exists.
//
// If the requested model contains a thinking suffix (e.g., "g25p(8192)"),
// the suffix is preserved in the returned model name (e.g., "gemini-2.5-pro(8192)").
// However, if the mapping target already contains a suffix, the config suffix
// takes priority over the user's suffix.
func (m *DefaultModelMapper) MapModel(requestedModel string) string {
	return m.MapModelWithInfo(requestedModel).Model
}

// MapModelWithInfo checks if a mapping exists for the requested model and if the
// target model has available local providers. It returns the mapped model name
// and matching mapping metadata, or an empty result if no valid mapping exists.
func (m *DefaultModelMapper) MapModelWithInfo(requestedModel string) MappingResult {
	if requestedModel == "" {
		return MappingResult{}
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	// Extract thinking suffix from requested model using ParseSuffix
	requestResult := thinking.ParseSuffix(requestedModel)
	baseModel := requestResult.ModelName

	// Normalize the base model for lookup (case-insensitive)
	normalizedBase := strings.ToLower(strings.TrimSpace(baseModel))

	// Check for direct mapping using base model name
	mapping, exists := m.mappings[normalizedBase]
	if !exists {
		// Try regex mappings in order using base model only
		// (suffix is handled separately via ParseSuffix)
		for _, rm := range m.regexps {
			if rm.re.MatchString(baseModel) {
				mapping = rm.mapping
				exists = true
				break
			}
		}
		if !exists {
			return MappingResult{}
		}
	}
	targetModel := strings.TrimSpace(mapping.To)

	// Check if target model already has a thinking suffix (config priority)
	targetResult := thinking.ParseSuffix(targetModel)

	// Verify target model has available providers (use base model for lookup)
	providers := util.GetProviderName(targetResult.ModelName)
	if len(providers) == 0 {
		log.Debugf("amp model mapping: target model %s has no available providers, skipping mapping", targetModel)
		return MappingResult{}
	}

	// Suffix handling: config suffix takes priority, otherwise preserve user suffix
	if targetResult.HasSuffix {
		// Config's "to" already contains a suffix - use it as-is (config priority)
		return MappingResult{Model: targetModel, Mapping: mapping}
	}

	// Preserve user's thinking suffix on the mapped model
	// (skip empty suffixes to avoid returning "model()")
	if requestResult.HasSuffix && requestResult.RawSuffix != "" {
		targetModel += "(" + requestResult.RawSuffix + ")"
	}

	// Note: Detailed routing log is handled by logAmpRouting in fallback_handlers.go
	return MappingResult{Model: targetModel, Mapping: mapping}
}

// UpdateMappings refreshes the mapping configuration from config.
// This is called during initialization and on config hot-reload.
func (m *DefaultModelMapper) UpdateMappings(mappings []config.AmpModelMapping) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Clear and rebuild mappings
	m.mappings = make(map[string]config.AmpModelMapping, len(mappings))
	m.regexps = make([]regexMapping, 0, len(mappings))

	for _, mapping := range mappings {
		from := strings.TrimSpace(mapping.From)
		to := strings.TrimSpace(mapping.To)

		if from == "" || to == "" {
			log.Warnf("amp model mapping: skipping invalid mapping (from=%q, to=%q)", from, to)
			continue
		}

		mapping.From = from
		mapping.To = to
		if len(mapping.ReasoningEffortMappings) > 0 {
			normalizedEfforts := make(map[string]string, len(mapping.ReasoningEffortMappings))
			for fromEffort, toEffort := range mapping.ReasoningEffortMappings {
				fromEffort = strings.ToLower(strings.TrimSpace(fromEffort))
				toEffort = strings.TrimSpace(toEffort)
				if fromEffort == "" || toEffort == "" {
					continue
				}
				normalizedEfforts[fromEffort] = toEffort
			}
			mapping.ReasoningEffortMappings = normalizedEfforts
		}
		if mapping.Regex {
			// Compile case-insensitive regex; wrap with (?i) to match behavior of exact lookups
			pattern := "(?i)" + from
			re, err := regexp.Compile(pattern)
			if err != nil {
				log.Warnf("amp model mapping: invalid regex %q: %v", from, err)
				continue
			}
			m.regexps = append(m.regexps, regexMapping{re: re, mapping: mapping})
			log.Debugf("amp model regex mapping registered: /%s/ -> %s", from, to)
		} else {
			// Store with normalized lowercase key for case-insensitive lookup
			normalizedFrom := strings.ToLower(from)
			m.mappings[normalizedFrom] = mapping
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
		result[k] = v.To
	}
	return result
}

type regexMapping struct {
	re      *regexp.Regexp
	mapping config.AmpModelMapping
}
