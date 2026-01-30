// Package amp provides model mapping functionality for routing Amp CLI requests
// to alternative models when the requested model is not available locally.
package amp

import (
	"regexp"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
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

	// oauthAliasForward maps channel -> name (lower) -> []alias for oauth-model-alias lookup.
	// This allows model-mappings targets to find providers via their aliases.
	oauthAliasForward map[string]map[string][]string
}

// NewModelMapper creates a new model mapper with the given initial mappings.
func NewModelMapper(mappings []config.AmpModelMapping) *DefaultModelMapper {
	m := &DefaultModelMapper{
		mappings:          make(map[string]string),
		regexps:           nil,
		oauthAliasForward: nil,
	}
	m.UpdateMappings(mappings)
	return m
}

// UpdateOAuthModelAlias updates the oauth-model-alias lookup table.
// This is called during initialization and on config hot-reload.
func (m *DefaultModelMapper) UpdateOAuthModelAlias(aliases map[string][]config.OAuthModelAlias) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(aliases) == 0 {
		m.oauthAliasForward = nil
		return
	}

	forward := make(map[string]map[string][]string, len(aliases))
	for rawChannel, entries := range aliases {
		channel := strings.ToLower(strings.TrimSpace(rawChannel))
		if channel == "" || len(entries) == 0 {
			continue
		}
		channelMap := make(map[string][]string)
		for _, entry := range entries {
			name := strings.TrimSpace(entry.Name)
			alias := strings.TrimSpace(entry.Alias)
			if name == "" || alias == "" {
				continue
			}
			if strings.EqualFold(name, alias) {
				continue
			}
			nameKey := strings.ToLower(name)
			channelMap[nameKey] = append(channelMap[nameKey], alias)
		}
		if len(channelMap) > 0 {
			forward[channel] = channelMap
		}
	}
	if len(forward) == 0 {
		m.oauthAliasForward = nil
		return
	}
	m.oauthAliasForward = forward
	log.Debugf("amp model mapping: loaded oauth-model-alias for %d channel(s)", len(forward))
}

// findProviderViaOAuthAlias checks if targetModel is an oauth-model-alias name
// and returns all aliases that have available providers.
// Returns the first alias and its providers for backward compatibility,
// and also populates allAliases with all available alias models.
func (m *DefaultModelMapper) findProviderViaOAuthAlias(targetModel string) (aliasModel string, providers []string) {
	aliases := m.findAllAliasesWithProviders(targetModel)
	if len(aliases) == 0 {
		return "", nil
	}
	// Return first one for backward compatibility
	first := aliases[0]
	return first, util.GetProviderName(first)
}

// findAllAliasesWithProviders returns all oauth-model-alias aliases for targetModel
// that have available providers. Useful for fallback when one alias is quota-exceeded.
func (m *DefaultModelMapper) findAllAliasesWithProviders(targetModel string) []string {
	if m.oauthAliasForward == nil {
		return nil
	}

	targetKey := strings.ToLower(strings.TrimSpace(targetModel))
	if targetKey == "" {
		return nil
	}

	var result []string
	seen := make(map[string]struct{})

	// Check all channels for this model name
	for _, channelMap := range m.oauthAliasForward {
		aliases := channelMap[targetKey]
		for _, alias := range aliases {
			aliasLower := strings.ToLower(alias)
			if _, exists := seen[aliasLower]; exists {
				continue
			}
			providers := util.GetProviderName(alias)
			if len(providers) > 0 {
				result = append(result, alias)
				seen[aliasLower] = struct{}{}
			}
		}
	}
	return result
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
	models := m.MapModelWithFallbacks(requestedModel)
	if len(models) == 0 {
		return ""
	}
	return models[0]
}

// MapModelWithFallbacks returns all possible target models for the requested model,
// including fallback aliases from oauth-model-alias. The first model is the primary target,
// and subsequent models are fallbacks to try if the primary is unavailable (e.g., quota exceeded).
func (m *DefaultModelMapper) MapModelWithFallbacks(requestedModel string) []string {
	if requestedModel == "" {
		return nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	// Extract thinking suffix from requested model using ParseSuffix
	requestResult := thinking.ParseSuffix(requestedModel)
	baseModel := requestResult.ModelName

	// Normalize the base model for lookup (case-insensitive)
	normalizedBase := strings.ToLower(strings.TrimSpace(baseModel))

	// Check for direct mapping using base model name
	targetModel, exists := m.mappings[normalizedBase]
	if !exists {
		// Try regex mappings in order using base model only
		// (suffix is handled separately via ParseSuffix)
		for _, rm := range m.regexps {
			if rm.re.MatchString(baseModel) {
				targetModel = rm.to
				exists = true
				break
			}
		}
		if !exists {
			return nil
		}
	}

	// Check if target model already has a thinking suffix (config priority)
	targetResult := thinking.ParseSuffix(targetModel)
	targetBase := targetResult.ModelName

	// Helper to apply suffix to a model
	applySuffix := func(model string) string {
		modelResult := thinking.ParseSuffix(model)
		if modelResult.HasSuffix {
			return model
		}
		if requestResult.HasSuffix && requestResult.RawSuffix != "" {
			return model + "(" + requestResult.RawSuffix + ")"
		}
		return model
	}

	// Verify target model has available providers (use base model for lookup)
	providers := util.GetProviderName(targetBase)

	// If direct provider available, return it as primary
	if len(providers) > 0 {
		return []string{applySuffix(targetModel)}
	}

	// No direct providers - check oauth-model-alias for all aliases that have providers
	allAliases := m.findAllAliasesWithProviders(targetBase)
	if len(allAliases) == 0 {
		log.Debugf("amp model mapping: target model %s has no available providers, skipping mapping", targetModel)
		return nil
	}

	// Log resolution
	if len(allAliases) == 1 {
		log.Debugf("amp model mapping: resolved %s -> %s via oauth-model-alias", targetModel, allAliases[0])
	} else {
		log.Debugf("amp model mapping: resolved %s -> %v via oauth-model-alias (%d fallbacks)", targetModel, allAliases, len(allAliases))
	}

	// Apply suffix to all aliases
	result := make([]string, len(allAliases))
	for i, alias := range allAliases {
		result[i] = applySuffix(alias)
	}
	return result
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
