package config

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
)

const (
	// DefaultModelFallbackMaxHops is used when routing.model-fallback.max-hops is unset.
	DefaultModelFallbackMaxHops = 2
	// MaxModelFallbackHops caps how many fallback models a single request may try.
	MaxModelFallbackHops = 8

	// ModelFallbackOnQuotaExhausted matches quota and rate-limit failures from any provider.
	ModelFallbackOnQuotaExhausted = "quota_exhausted"
	// ModelFallbackOnAuthUnavailable matches requests where no credential could be selected.
	ModelFallbackOnAuthUnavailable = "auth_unavailable"
	// ModelFallbackOn5xx matches every upstream 5xx status.
	ModelFallbackOn5xx = "5xx"
)

// DefaultModelFallbackOn lists the conditions used when routing.model-fallback.on is omitted.
var DefaultModelFallbackOn = []string{"429", ModelFallbackOnQuotaExhausted, ModelFallbackOnAuthUnavailable, "529"}

// ModelFallbackConfig configures declarative cross-model fallback chains.
// After every credential of the requested model is exhausted, the same request is
// retried with the next model of its chain when the final error matches On.
type ModelFallbackConfig struct {
	// Enabled turns model fallback on. Default: false.
	Enabled bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// MaxHops caps how many fallback models are tried per request. Default: 2, maximum: 8.
	MaxHops int `yaml:"max-hops,omitempty" json:"max-hops,omitempty"`
	// On lists the eligible failure conditions: HTTP statuses (408, 429, 5xx codes),
	// "5xx", "quota_exhausted", and "auth_unavailable". Default: DefaultModelFallbackOn.
	On []string `yaml:"on" json:"on"`
	// Chains maps a requested model to its ordered fallback models.
	Chains map[string][]string `yaml:"chains,omitempty" json:"chains,omitempty"`
}

// ModelFallbackConditions is the parsed form of ModelFallbackConfig.On.
type ModelFallbackConditions struct {
	Statuses        map[int]bool
	Any5xx          bool
	QuotaExhausted  bool
	AuthUnavailable bool
}

// normalizeModelFallbackCondition canonicalizes one condition token.
// It returns false for unsupported tokens.
func normalizeModelFallbackCondition(raw string) (string, bool) {
	token := strings.ToLower(strings.TrimSpace(raw))
	switch token {
	case ModelFallbackOnQuotaExhausted, ModelFallbackOnAuthUnavailable, ModelFallbackOn5xx:
		return token, true
	}
	status, err := strconv.Atoi(token)
	if err != nil {
		return "", false
	}
	// Request faults (400/401/403/404/413/422, ...) never justify switching models.
	if status == 408 || status == 429 || (status >= 500 && status <= 599) {
		return strconv.Itoa(status), true
	}
	return "", false
}

// Validate reports configuration errors for management writes.
func (c ModelFallbackConfig) Validate() error {
	if c.MaxHops < 0 || c.MaxHops > MaxModelFallbackHops {
		return fmt.Errorf("routing.model-fallback.max-hops must be between 0 and %d", MaxModelFallbackHops)
	}
	for _, raw := range c.On {
		if _, ok := normalizeModelFallbackCondition(raw); !ok {
			return fmt.Errorf("routing.model-fallback.on: unsupported condition %q (use 408, 429, 5xx codes, 5xx, quota_exhausted, auth_unavailable)", raw)
		}
	}
	for source, targets := range c.Chains {
		if strings.TrimSpace(source) == "" {
			return fmt.Errorf("routing.model-fallback.chains: model name must not be empty")
		}
		if len(targets) == 0 {
			return fmt.Errorf("routing.model-fallback.chains.%s: at least one fallback model is required", source)
		}
		for _, target := range targets {
			if strings.TrimSpace(target) == "" {
				return fmt.Errorf("routing.model-fallback.chains.%s: fallback model must not be empty", source)
			}
			if strings.EqualFold(strings.TrimSpace(target), strings.TrimSpace(source)) {
				return fmt.Errorf("routing.model-fallback.chains.%s: a model cannot fall back to itself", source)
			}
		}
	}
	return nil
}

// SanitizeModelFallback normalizes routing.model-fallback and drops invalid entries.
func (cfg *Config) SanitizeModelFallback() {
	if cfg == nil {
		return
	}
	fallback := &cfg.Routing.ModelFallback
	if fallback.MaxHops < 0 {
		fallback.MaxHops = 0
	}
	if fallback.MaxHops > MaxModelFallbackHops {
		fallback.MaxHops = MaxModelFallbackHops
	}
	if len(fallback.On) > 0 {
		seen := make(map[string]struct{}, len(fallback.On))
		on := make([]string, 0, len(fallback.On))
		for _, raw := range fallback.On {
			token, ok := normalizeModelFallbackCondition(raw)
			if !ok {
				log.Warnf("routing.model-fallback.on: ignoring unsupported condition %q", raw)
				continue
			}
			if _, dup := seen[token]; dup {
				continue
			}
			seen[token] = struct{}{}
			on = append(on, token)
		}
		fallback.On = on
	}
	if len(fallback.Chains) == 0 {
		fallback.Chains = nil
		return
	}
	chains := make(map[string][]string, len(fallback.Chains))
	for rawSource, rawTargets := range fallback.Chains {
		source := strings.TrimSpace(rawSource)
		if source == "" {
			continue
		}
		seen := map[string]struct{}{strings.ToLower(source): {}}
		targets := make([]string, 0, len(rawTargets))
		for _, rawTarget := range rawTargets {
			target := strings.TrimSpace(rawTarget)
			key := strings.ToLower(target)
			if target == "" {
				continue
			}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			targets = append(targets, target)
		}
		if len(targets) > 0 {
			chains[source] = targets
		}
	}
	fallback.Chains = chains
}

// EffectiveMaxHops returns the configured hop limit or its default.
func (c ModelFallbackConfig) EffectiveMaxHops() int {
	if c.MaxHops <= 0 {
		return DefaultModelFallbackMaxHops
	}
	if c.MaxHops > MaxModelFallbackHops {
		return MaxModelFallbackHops
	}
	return c.MaxHops
}

// Conditions parses On, falling back to DefaultModelFallbackOn when it is omitted.
func (c ModelFallbackConfig) Conditions() ModelFallbackConditions {
	tokens := c.On
	if tokens == nil {
		tokens = DefaultModelFallbackOn
	}
	out := ModelFallbackConditions{Statuses: make(map[int]bool, len(tokens))}
	for _, raw := range tokens {
		token, ok := normalizeModelFallbackCondition(raw)
		if !ok {
			continue
		}
		switch token {
		case ModelFallbackOnQuotaExhausted:
			out.QuotaExhausted = true
		case ModelFallbackOnAuthUnavailable:
			out.AuthUnavailable = true
		case ModelFallbackOn5xx:
			out.Any5xx = true
		default:
			status, _ := strconv.Atoi(token)
			out.Statuses[status] = true
		}
	}
	return out
}

// Targets returns the fallback models configured for model, in order.
// The chain key matches the full model name first and then its base name without a
// thinking suffix; a request suffix such as "(high)" is carried to targets without one.
func (c ModelFallbackConfig) Targets(model string) []string {
	if !c.Enabled || len(c.Chains) == 0 {
		return nil
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	base, suffix, hasSuffix := splitModelFallbackSuffix(model)
	targets := lookupModelFallbackChain(c.Chains, model)
	if len(targets) == 0 && hasSuffix {
		targets = lookupModelFallbackChain(c.Chains, base)
	}
	if len(targets) == 0 {
		return nil
	}
	out := make([]string, 0, len(targets))
	for _, target := range targets {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		if _, _, targetHasSuffix := splitModelFallbackSuffix(target); hasSuffix && !targetHasSuffix {
			target = fmt.Sprintf("%s(%s)", target, suffix)
		}
		out = append(out, target)
	}
	return out
}

func lookupModelFallbackChain(chains map[string][]string, model string) []string {
	if targets, ok := chains[model]; ok {
		return targets
	}
	// Case-insensitive match with deterministic order for keys differing only by case.
	keys := make([]string, 0, len(chains))
	for key := range chains {
		if strings.EqualFold(strings.TrimSpace(key), model) {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	return chains[keys[0]]
}

// splitModelFallbackSuffix mirrors thinking.ParseSuffix ("model(value)"), which
// cannot be imported here without an import cycle.
func splitModelFallbackSuffix(model string) (string, string, bool) {
	open := strings.LastIndex(model, "(")
	if open < 0 || !strings.HasSuffix(model, ")") {
		return model, "", false
	}
	return model[:open], model[open+1 : len(model)-1], true
}
