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

	// MapModelCtx is the feature-aware variant. Selection follows the
	// grouped semantics documented on selectTarget: exact groups before
	// regex groups; within a group, the first conditional rule (in
	// declaration order) whose When matches and whose target is
	// available wins, otherwise the first available unconditional
	// fallback in the same group is used.
	MapModelCtx(requestedModel string, fp RequestFingerprint) string

	// UpdateMappings refreshes the mapping configuration (for hot-reload).
	UpdateMappings(mappings []config.AmpModelMapping)
}

// DefaultModelMapper implements ModelMapper with thread-safe mapping storage.
//
// Selection order (see selectTarget for the full algorithm):
//  1. exact rules
//  2. regex rules
//
// Within each pass, matching rules are bucketed by their normalized
// From key into groups (in declaration order of each group's first
// matching rule). Groups are then evaluated in order: a group's first
// conditional rule whose When matches and whose target has registered
// providers wins; otherwise the first available unconditional rule in
// that group is used as fallback. Across groups, an earlier group's
// available fallback is returned before any later group's conditional
// rule is considered.
type DefaultModelMapper struct {
	mu     sync.RWMutex
	exacts []mappingRule
	regexs []mappingRule
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
// Equivalent to MapModelCtx with an empty fingerprint, so conditions
// requiring a non-empty Feature/ToolChoice/UserSuffix/SystemPrefix
// will not match (rules with a nil When still apply normally).
func (m *DefaultModelMapper) MapModel(requestedModel string) string {
	return m.MapModelCtx(requestedModel, RequestFingerprint{})
}

// MapModelCtx checks if a mapping exists for the requested model and if the
// target model has available local providers. Conditional rules (When) are
// evaluated against fp; an unconditional fallback for the same From is used
// when no conditional rule matches.
//
// If the requested model contains a thinking suffix (e.g., "g25p(8192)"),
// the suffix is preserved in the returned model name (e.g., "gemini-2.5-pro(8192)").
// However, if the mapping target already contains a suffix, the config suffix
// takes priority over the user's suffix.
func (m *DefaultModelMapper) MapModelCtx(requestedModel string, fp RequestFingerprint) string {
	if requestedModel == "" {
		return ""
	}

	// Extract thinking suffix from requested model using ParseSuffix
	requestResult := thinking.ParseSuffix(requestedModel)
	baseModel := requestResult.ModelName
	normalizedBase := strings.ToLower(strings.TrimSpace(baseModel))

	m.mu.RLock()
	targetModel := selectTarget(m.exacts, baseModel, normalizedBase, fp, false, hasProviders)
	if targetModel == "" {
		targetModel = selectTarget(m.regexs, baseModel, normalizedBase, fp, true, hasProviders)
	}
	m.mu.RUnlock()

	if targetModel == "" {
		return ""
	}

	// Target was already validated by selectTarget; ParseSuffix again
	// only to decide on suffix-merge behavior below.
	targetResult := thinking.ParseSuffix(targetModel)

	// Suffix handling: config suffix takes priority, otherwise preserve user suffix
	if targetResult.HasSuffix {
		return targetModel
	}
	if requestResult.HasSuffix && requestResult.RawSuffix != "" {
		return targetModel + "(" + requestResult.RawSuffix + ")"
	}
	return targetModel
}

// hasProviders reports whether a target model (possibly with a thinking
// suffix) has any registered local providers. Used by selectTarget to
// skip rules whose target is unavailable so that fallback rules can
// still apply.
func hasProviders(targetModel string) bool {
	if targetModel == "" {
		return false
	}
	res := thinking.ParseSuffix(targetModel)
	return len(util.GetProviderName(res.ModelName)) > 0
}

// selectTarget scans rules of one class (exact or regex) and returns the
// best target model name with the following semantics:
//
//   - Rules are grouped by their normalized From key. A group's
//     "declaration order" is the position of its first matching rule
//     in the input slice.
//   - Within one group, the first conditional rule (in declaration
//     order) whose When matches the fingerprint and whose target is
//     reported available wins. This holds regardless of where the
//     conditional and unconditional rules are interleaved (so a
//     same-key conditional appended later, e.g. via PATCH, is still
//     reachable even if rules from another From appear in between).
//   - If no conditional rule wins, the group's fallback is the first
//     unconditional rule in declaration order whose target is
//     available.
//   - Across groups, the earliest-declared group is tried first: if
//     its conditional matches, use that; otherwise if it has an
//     available unconditional fallback, use that; otherwise move on
//     to the next group in declaration order.
//   - A rule whose `to` model has no local providers (per `available`)
//     is skipped during both conditional matching and fallback
//     selection.
//
// Returns "" if no rule produces an available target.
func selectTarget(rules []mappingRule, baseModel, normalizedBase string, fp RequestFingerprint, isRegex bool, available func(string) bool) string {
	type group struct {
		key         string
		conditional []mappingRule // ordered as declared, only matching this baseModel
		fallback    string        // first available unconditional `to`
	}
	groups := make([]*group, 0)
	groupIdx := make(map[string]int)
	for _, r := range rules {
		var key string
		if isRegex {
			if r.re == nil || !r.re.MatchString(baseModel) {
				continue
			}
			// Regex patterns are compiled case-insensitively (UpdateMappings
			// prefixes "(?i)"), so two rules differing only by letter case
			// match the same inputs and must share a group. Lower-case the
			// pattern string when grouping so case-variant From values do
			// not split into separate groups and hide a same-From
			// conditional behind an earlier fallback.
			key = strings.ToLower(r.re.String())
		} else {
			if r.exactFrom == "" || r.exactFrom != normalizedBase {
				continue
			}
			key = r.exactFrom
		}
		idx, ok := groupIdx[key]
		if !ok {
			groups = append(groups, &group{key: key})
			idx = len(groups) - 1
			groupIdx[key] = idx
		}
		g := groups[idx]
		if IsConditionEffectivelyEmpty(r.when) {
			if g.fallback == "" && available(r.to) {
				g.fallback = r.to
			}
			continue
		}
		g.conditional = append(g.conditional, r)
	}
	for _, g := range groups {
		for _, r := range g.conditional {
			if ConditionMatches(r.when, fp) && available(r.to) {
				return r.to
			}
		}
		if g.fallback != "" {
			return g.fallback
		}
	}
	return ""
}

// UpdateMappings refreshes the mapping configuration from config.
// This is called during initialization and on config hot-reload.
func (m *DefaultModelMapper) UpdateMappings(mappings []config.AmpModelMapping) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.exacts = m.exacts[:0]
	m.regexs = m.regexs[:0]

	conditional := 0
	for _, mapping := range mappings {
		from := strings.TrimSpace(mapping.From)
		to := strings.TrimSpace(mapping.To)
		if from == "" || to == "" {
			log.Warnf("amp model mapping: skipping invalid mapping (from=%q, to=%q)", from, to)
			continue
		}

		// Deep-copy When so the mapper does not share state with the
		// caller's config object.
		var when *config.AmpMappingCondition
		if mapping.When != nil {
			c := *mapping.When
			when = &c
		}
		rule := mappingRule{to: to, when: when}

		if mapping.Regex {
			pattern := "(?i)" + from
			re, err := regexp.Compile(pattern)
			if err != nil {
				log.Warnf("amp model mapping: invalid regex %q: %v", from, err)
				continue
			}
			rule.re = re
			m.regexs = append(m.regexs, rule)
			log.Debugf("amp model regex mapping registered: /%s/ -> %s (when=%+v)", from, to, when)
		} else {
			rule.exactFrom = strings.ToLower(from)
			m.exacts = append(m.exacts, rule)
			log.Debugf("amp model mapping registered: %s -> %s (when=%+v)", from, to, when)
		}
		if when != nil {
			conditional++
		}
	}

	if n := len(m.exacts); n > 0 {
		log.Infof("amp model mapping: loaded %d mapping(s)", n)
	}
	if n := len(m.regexs); n > 0 {
		log.Infof("amp model mapping: loaded %d regex mapping(s)", n)
	}
	if conditional > 0 {
		log.Infof("amp model mapping: %d mapping(s) are feature-conditional", conditional)
	}
}

// GetMappings returns a snapshot of current unconditional exact mappings
// (for debugging/status). Conditional and regex rules are excluded; they
// can be inspected via configuration.
func (m *DefaultModelMapper) GetMappings() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]string)
	for _, r := range m.exacts {
		if r.when != nil {
			continue
		}
		// First wins; do not overwrite existing entries.
		if _, ok := result[r.exactFrom]; !ok {
			result[r.exactFrom] = r.to
		}
	}
	return result
}
