// Package amp provides model mapping functionality for routing Amp CLI requests
// to alternative models when the requested model is not available locally.
package amp

import (
	"regexp"
	"strings"
	"sync"

	"github.com/kooshapari/cliproxyapi-plusplus/v6/internal/config"
	"github.com/kooshapari/cliproxyapi-plusplus/v6/pkg/llmproxy/thinking"
	"github.com/kooshapari/cliproxyapi-plusplus/v6/pkg/llmproxy/util"
	log "github.com/sirupsen/logrus"
)

// ModelMapper provides model name mapping/aliasing for Amp CLI requests.
// When an Amp request comes in for a model that isn't available locally,
// this mapper can redirect it to an alternative model that IS available.
type ModelMapper interface {
	// MapModel returns the target model name if a mapping exists and the target
	// model has available providers. Returns empty string if no mapping applies.
	MapModel(requestedModel string) string

	// MapModelWithParams returns the target model name and any configured params
	// to inject when the mapping applies. Returns empty string if no mapping applies.
	MapModelWithParams(requestedModel string) (string, map[string]interface{})

	// UpdateMappings refreshes the mapping configuration (for hot-reload).
	UpdateMappings(mappings []config.AmpModelMapping)
}

// DefaultModelMapper implements ModelMapper with thread-safe mapping storage.
type DefaultModelMapper struct {
	mu       sync.RWMutex
	mappings map[string]modelMappingValue // exact: from -> value (normalized lowercase keys)
	regexps  []regexMapping               // regex rules evaluated in order
}

type modelMappingValue struct {
	to     string
	params map[string]interface{}
}

// NewModelMapper creates a new model mapper with the given initial mappings.
func NewModelMapper(mappings []config.AmpModelMapping) *DefaultModelMapper {
	m := &DefaultModelMapper{
		mappings: make(map[string]modelMappingValue),
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
	mappedModel, _ := m.MapModelWithParams(requestedModel)
	return mappedModel
}

// MapModelWithParams resolves a mapping and returns both the target model and mapping params.
// Params are copied for caller safety.
func (m *DefaultModelMapper) MapModelWithParams(requestedModel string) (string, map[string]interface{}) {
	if requestedModel == "" {
		return "", nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	// Extract thinking suffix from requested model using ParseSuffix.
	requestResult := thinking.ParseSuffix(requestedModel)
	baseModel := requestResult.ModelName
	normalizedBase := strings.ToLower(strings.TrimSpace(baseModel))

	// Resolve exact mapping first.
	mapping, exists := m.mappings[normalizedBase]
	if !exists {
		// Try regex mappings in order using base model only.
		for _, rm := range m.regexps {
			if rm.re.MatchString(baseModel) {
				mapping = rm.to
				exists = true
				break
			}
		}
	}
	if !exists {
		return "", nil
	}

	targetModel := mapping.to
	targetResult := thinking.ParseSuffix(targetModel)

	// Validate target model availability before returning a mapping.
	providers := util.GetProviderName(targetResult.ModelName)
	if len(providers) == 0 {
		log.Debugf("amp model mapping: target model %s has no available providers, skipping mapping", targetModel)
		return "", nil
	}

	mappedParams := copyMappingParams(mapping.params)

	// Suffix handling: config suffix takes priority.
	if targetResult.HasSuffix {
		return targetModel, mappedParams
	}

	if requestResult.HasSuffix && requestResult.RawSuffix != "" {
		return targetModel + "(" + requestResult.RawSuffix + ")", mappedParams
	}

	return targetModel, mappedParams
}

func copyMappingParams(src map[string]interface{}) map[string]interface{} {
	if len(src) == 0 {
		return nil
	}

	dst := make(map[string]interface{}, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// UpdateMappings refreshes the mapping configuration from config.
// This is called during initialization and on config hot-reload.
func (m *DefaultModelMapper) UpdateMappings(mappings []config.AmpModelMapping) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.mappings = make(map[string]modelMappingValue, len(mappings))
	m.regexps = make([]regexMapping, 0, len(mappings))

	for _, mapping := range mappings {
		from := strings.TrimSpace(mapping.From)
		to := strings.TrimSpace(mapping.To)

		if from == "" || to == "" {
			log.Warnf("amp model mapping: skipping invalid mapping (from=%q, to=%q)", from, to)
			continue
		}

		params := copyMappingParams(mapping.Params)
		value := modelMappingValue{
			to:     to,
			params: params,
		}

		if mapping.Regex {
			pattern := "(?i)" + from
			re, err := regexp.Compile(pattern)
			if err != nil {
				log.Warnf("amp model mapping: invalid regex %q: %v", from, err)
				continue
			}
			m.regexps = append(m.regexps, regexMapping{re: re, to: value})
			log.Debugf("amp model regex mapping registered: /%s/ -> %s", from, to)
			continue
		}

		normalizedFrom := strings.ToLower(from)
		m.mappings[normalizedFrom] = value
		log.Debugf("amp model mapping registered: %s -> %s", from, to)
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
		result[k] = v.to
	}
	return result
}

type regexMapping struct {
	re *regexp.Regexp
	to modelMappingValue
}
