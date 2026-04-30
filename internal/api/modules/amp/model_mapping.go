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
	// Equivalent to MapModelCtx with an empty fingerprint.
	MapModel(requestedModel string) string

	// MapModelCtx is the feature-aware variant. Conditional mappings (When)
	// are evaluated against fp; the first matching rule wins.
	MapModelCtx(requestedModel string, fp RequestFingerprint) string

	// UpdateMappings refreshes the mapping configuration (for hot-reload).
	UpdateMappings(mappings []config.AmpModelMapping)
}

// DefaultModelMapper implements ModelMapper with thread-safe mapping storage.
//
// Mappings are stored in declaration order so that conditional rules ("When")
// can be evaluated first and an unconditional rule for the same From acts as
// a fallback. Lookups iterate the slice in order.
type DefaultModelMapper struct {
	mu    sync.RWMutex
	rules []mappingRule
}

// mappingRule is a normalized form of a single AmpModelMapping entry.
type mappingRule struct {
	exactFrom string                      // lower-cased exact from, "" if regex
	re        *regexp.Regexp              // compiled regex, nil if exact
	to        string                      // raw target (may include thinking suffix)
	when      *config.AmpMappingCondition // optional condition
}

// NewModelMapper creates a new model mapper with the given initial mappings.
func NewModelMapper(mappings []config.AmpModelMapping) *DefaultModelMapper {
	m := &DefaultModelMapper{}
	m.UpdateMappings(mappings)
	return m
}

// MapModel is a convenience wrapper for callers that have no fingerprint.
// Conditional rules are skipped.
func (m *DefaultModelMapper) MapModel(requestedModel string) string {
	return m.MapModelCtx(requestedModel, RequestFingerprint{})
}

// MapModelCtx checks if a mapping exists for the requested model and if the
// target model has available local providers. Conditional rules (When) are
// evaluated against fp; rules with a non-matching condition are skipped.
//
// If the requested model contains a thinking suffix (e.g., "g25p(8192)"),
// the suffix is preserved in the returned model name (e.g., "gemini-2.5-pro(8192)").
// However, if the mapping target already contains a suffix, the config suffix
// takes priority over the user's suffix.
func (m *DefaultModelMapper) MapModelCtx(requestedModel string, fp RequestFingerprint) string {
	if requestedModel == "" {
		return ""
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	// Extract thinking suffix from requested model using ParseSuffix
	requestResult := thinking.ParseSuffix(requestedModel)
	baseModel := requestResult.ModelName
	normalizedBase := strings.ToLower(strings.TrimSpace(baseModel))

	targetModel := ""
	for _, r := range m.rules {
		if r.exactFrom != "" {
			if r.exactFrom != normalizedBase {
				continue
			}
		} else if r.re != nil {
			if !r.re.MatchString(baseModel) {
				continue
			}
		} else {
			continue
		}
		if !ConditionMatches(r.when, fp) {
			continue
		}
		targetModel = r.to
		break
	}
	if targetModel == "" {
		return ""
	}

	// Check if target model already has a thinking suffix (config priority)
	targetResult := thinking.ParseSuffix(targetModel)

	// Verify target model has available providers (use base model for lookup)
	providers := util.GetProviderName(targetResult.ModelName)
	if len(providers) == 0 {
		log.Debugf("amp model mapping: target model %s has no available providers, skipping mapping", targetModel)
		return ""
	}

	// Suffix handling: config suffix takes priority, otherwise preserve user suffix
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
func (m *DefaultModelMapper) UpdateMappings(mappings []config.AmpModelMapping) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.rules = make([]mappingRule, 0, len(mappings))

	exact := 0
	regex := 0
	conditional := 0
	for _, mapping := range mappings {
		from := strings.TrimSpace(mapping.From)
		to := strings.TrimSpace(mapping.To)
		if from == "" || to == "" {
			log.Warnf("amp model mapping: skipping invalid mapping (from=%q, to=%q)", from, to)
			continue
		}

		rule := mappingRule{to: to, when: mapping.When}

		if mapping.Regex {
			pattern := "(?i)" + from
			re, err := regexp.Compile(pattern)
			if err != nil {
				log.Warnf("amp model mapping: invalid regex %q: %v", from, err)
				continue
			}
			rule.re = re
			regex++
			log.Debugf("amp model regex mapping registered: /%s/ -> %s (when=%v)", from, to, mapping.When)
		} else {
			rule.exactFrom = strings.ToLower(from)
			exact++
			log.Debugf("amp model mapping registered: %s -> %s (when=%v)", from, to, mapping.When)
		}
		if mapping.When != nil {
			conditional++
		}
		m.rules = append(m.rules, rule)
	}

	if exact > 0 {
		log.Infof("amp model mapping: loaded %d mapping(s)", exact)
	}
	if regex > 0 {
		log.Infof("amp model mapping: loaded %d regex mapping(s)", regex)
	}
	if conditional > 0 {
		log.Infof("amp model mapping: %d mapping(s) are feature-conditional", conditional)
	}
}

// GetMappings returns a snapshot of current exact mappings (for debugging/status).
// Conditional rules and regex rules are not included; they can be inspected via
// configuration.
func (m *DefaultModelMapper) GetMappings() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]string)
	for _, r := range m.rules {
		if r.exactFrom == "" || r.when != nil {
			continue
		}
		// First wins; do not overwrite existing entries.
		if _, ok := result[r.exactFrom]; !ok {
			result[r.exactFrom] = r.to
		}
	}
	return result
}
