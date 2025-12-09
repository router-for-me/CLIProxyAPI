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
	overrides []config.AmpModelMapping
	patterns  []*compiledPattern
	mu        sync.RWMutex
}

type compiledPattern struct {
	from    string
	to      string
	regex   *regexp.Regexp
	isExact bool
}

// NewModelMapper creates a new model mapper with the given initial mappings.
func NewModelMapper(mappings []config.AmpModelMapping) *DefaultModelMapper {
	m := &DefaultModelMapper{
		overrides: mappings,
	}
	m.compilePatterns()
	return m
}

// MapModel checks if a mapping exists for the requested model and if the
// target model has available local providers. Returns the mapped model name
// or empty string if no valid mapping exists.
func (r *DefaultModelMapper) MapModel(model string) string {
	if r == nil || len(r.patterns) == 0 {
		return model
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, cp := range r.patterns {
		if cp.isExact {
			if cp.from == model {
				log.Debugf("amp model override: %s -> %s (exact match)", model, cp.to)
				return cp.to
			}
		} else if cp.regex != nil {
			if cp.regex.MatchString(model) {
				log.Debugf("amp model override: %s -> %s (pattern: %s)", model, cp.to, cp.from)
				return cp.to
			}
		}
	}

	// Verify target model has available providers
	providers := util.GetProviderName(model)
	if len(providers) == 0 {
		log.Debugf("amp model mapping: target model %s has no available providers, skipping mapping", model)
		return ""
	}

	// Note: Detailed routing log is handled by logAmpRouting in fallback_handlers.go
	return model
}

// UpdateMappings refreshes the mapping configuration from config.
// This is called during initialization and on config hot-reload.
func (r *DefaultModelMapper) UpdateMappings(mappings []config.AmpModelMapping) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.overrides = mappings
	r.compilePatterns()
	log.Debugf("amp model override: updated with %d rules", len(r.patterns))
}

// compilePatterns pre-compiles wildcard patterns to regex for efficient matching.
func (r *DefaultModelMapper) compilePatterns() {
	r.patterns = make([]*compiledPattern, 0, len(r.overrides))
	for _, override := range r.overrides {
		cp := &compiledPattern{
			from: override.From,
			to:   override.To,
		}

		if !strings.Contains(override.From, "*") {
			cp.isExact = true
		} else {
			pattern := globToRegex(override.From)
			regex, err := regexp.Compile(pattern)
			if err != nil {
				log.Warnf("amp model override: invalid pattern %q, skipping: %v", override.From, err)
				continue
			}
			cp.regex = regex
		}
		r.patterns = append(r.patterns, cp)
	}
}

// globToRegex converts a glob pattern with * wildcards to a regex pattern.
func globToRegex(glob string) string {
	var result strings.Builder
	result.WriteString("^")
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch c {
		case '*':
			result.WriteString(".*")
		case '.', '+', '?', '[', ']', '(', ')', '{', '}', '^', '$', '|', '\\':
			result.WriteByte('\\')
			result.WriteByte(c)
		default:
			result.WriteByte(c)
		}
	}
	result.WriteString("$")
	return result.String()
}

// GetMappings returns a copy of current mappings (for debugging/status).
// Returns map with pattern (from) as key and target model (to) as value.
func (m *DefaultModelMapper) GetMappings() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]string, len(m.patterns))
	for _, cp := range m.patterns {
		result[cp.from] = cp.to
	}
	return result
}
