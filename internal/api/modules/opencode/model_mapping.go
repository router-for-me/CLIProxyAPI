// Package opencode implements the OpenCode agent routing module. OpenCode
// (https://opencode.ai) is an OpenAI/Anthropic-compatible coding agent; this
// module exposes a dedicated /opencode/... route namespace (merged and
// provider-scoped) backed by the existing SDK handlers, plus an optional
// model-mapping layer scoped to OpenCode traffic.
package opencode

import (
	"regexp"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

// ModelMapper provides model name mapping/aliasing for OpenCode requests.
// When an OpenCode request comes in for a model that isn't available locally,
// this mapper can redirect it to an alternative model that IS available.
type ModelMapper interface {
	// MapModel returns the target model name if a mapping exists and the target
	// model has available providers. Returns empty string if no mapping applies.
	MapModel(requestedModel string) string

	// UpdateMappings refreshes the mapping configuration (for hot-reload).
	UpdateMappings(mappings []config.OpenCodeModelMapping)
}

// DefaultModelMapper implements ModelMapper with thread-safe mapping storage.
type DefaultModelMapper struct {
	mu       sync.RWMutex
	mappings map[string]string // exact: from -> to (normalized lowercase keys)
	regexps  []regexMapping    // regex rules evaluated in order
}

type regexMapping struct {
	re *regexp.Regexp
	to string
}

// NewModelMapper creates a new model mapper with the given initial mappings.
func NewModelMapper(mappings []config.OpenCodeModelMapping) *DefaultModelMapper {
	m := &DefaultModelMapper{
		mappings: make(map[string]string),
	}
	m.UpdateMappings(mappings)
	return m
}

// MapModel checks if a mapping exists for the requested model and if the target
// model has available local providers. Returns the mapped model name or empty
// string if no valid mapping exists.
//
// If the requested model contains a thinking suffix (e.g., "gpt-5(8192)"), the
// suffix is preserved in the returned model name unless the mapping target
// already specifies its own suffix (config priority).
func (m *DefaultModelMapper) MapModel(requestedModel string) string {
	if requestedModel == "" {
		return ""
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	// Extract thinking suffix from requested model using ParseSuffix.
	requestResult := thinking.ParseSuffix(requestedModel)
	baseModel := requestResult.ModelName

	// Normalize the base model for lookup (case-insensitive).
	normalizedBase := strings.ToLower(strings.TrimSpace(baseModel))

	// Check for direct mapping using base model name.
	targetModel, exists := m.mappings[normalizedBase]
	if !exists {
		// Try regex mappings in order using base model only.
		for _, rm := range m.regexps {
			if rm.re.MatchString(baseModel) {
				targetModel = rm.to
				exists = true
				break
			}
		}
		if !exists {
			return ""
		}
	}

	// Check if target model already has a thinking suffix (config priority).
	targetResult := thinking.ParseSuffix(targetModel)

	// Verify target model has available providers (use base model for lookup).
	providers := util.GetProviderName(targetResult.ModelName)
	if len(providers) == 0 {
		log.Debugf("opencode model mapping: target model %s has no available providers, skipping mapping", targetModel)
		return ""
	}

	// Suffix handling: config suffix takes priority, otherwise preserve user suffix.
	if targetResult.HasSuffix {
		return targetModel
	}
	if requestResult.HasSuffix && requestResult.RawSuffix != "" {
		return targetModel + "(" + requestResult.RawSuffix + ")"
	}
	return targetModel
}

// UpdateMappings refreshes the mapping configuration from config.
// This is called during initialization and on config hot-reload.
func (m *DefaultModelMapper) UpdateMappings(mappings []config.OpenCodeModelMapping) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.mappings = make(map[string]string, len(mappings))
	m.regexps = make([]regexMapping, 0, len(mappings))

	for _, mapping := range mappings {
		from := strings.TrimSpace(mapping.From)
		to := strings.TrimSpace(mapping.To)
		if from == "" || to == "" {
			log.Warnf("opencode model mapping: skipping invalid mapping (from=%q, to=%q)", from, to)
			continue
		}

		if mapping.Regex {
			// Compile case-insensitive regex to match the exact-lookup behavior.
			re, err := regexp.Compile("(?i)" + from)
			if err != nil {
				log.Warnf("opencode model mapping: invalid regex %q: %v", from, err)
				continue
			}
			m.regexps = append(m.regexps, regexMapping{re: re, to: to})
			log.Debugf("opencode model regex mapping registered: /%s/ -> %s", from, to)
			continue
		}

		normalizedFrom := strings.ToLower(from)
		m.mappings[normalizedFrom] = to
		log.Debugf("opencode model mapping registered: %s -> %s", from, to)
	}

	if len(m.mappings) > 0 {
		log.Infof("opencode model mapping: loaded %d mapping(s)", len(m.mappings))
	}
	if n := len(m.regexps); n > 0 {
		log.Infof("opencode model mapping: loaded %d regex mapping(s)", n)
	}
}

// GetMappings returns a copy of the current exact mappings (for debugging/status).
func (m *DefaultModelMapper) GetMappings() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]string, len(m.mappings))
	for k, v := range m.mappings {
		result[k] = v
	}
	return result
}
