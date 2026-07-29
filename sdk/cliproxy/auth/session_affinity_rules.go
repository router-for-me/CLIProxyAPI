package auth

import (
	"strings"
)

// SessionAffinityRuleLimit is a compiled max-requests override for matching traffic.
type SessionAffinityRuleLimit struct {
	Provider    string
	Model       string
	MaxRequests int
}

// normalizeSessionAffinityMaxRequests maps config values to runtime form.
// Values <= 0 mean unlimited and are stored as -1.
func normalizeSessionAffinityMaxRequests(maxRequests int) int {
	if maxRequests <= 0 {
		return -1
	}
	return maxRequests
}

// CompileSessionAffinityRules normalizes rules (provider/model keys, max-requests) and drops empty entries.
func CompileSessionAffinityRules(rules []SessionAffinityRuleLimit) []SessionAffinityRuleLimit {
	return compileSessionAffinityRules(rules)
}

// compileSessionAffinityRules normalizes rules and drops empty entries.
func compileSessionAffinityRules(rules []SessionAffinityRuleLimit) []SessionAffinityRuleLimit {
	if len(rules) == 0 {
		return nil
	}
	out := make([]SessionAffinityRuleLimit, 0, len(rules))
	for _, rule := range rules {
		provider := strings.ToLower(strings.TrimSpace(rule.Provider))
		model := strings.ToLower(strings.TrimSpace(canonicalModelKey(rule.Model)))
		if provider == "" && model == "" {
			continue
		}
		out = append(out, SessionAffinityRuleLimit{
			Provider:    provider,
			Model:       model,
			MaxRequests: normalizeSessionAffinityMaxRequests(rule.MaxRequests),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// resolveSessionAffinityMaxRequests picks the most specific matching rule, else the global default.
// Specificity: provider+model > model-only > provider-only > global.
func resolveSessionAffinityMaxRequests(global int, rules []SessionAffinityRuleLimit, provider, model string) int {
	global = normalizeSessionAffinityMaxRequests(global)
	if len(rules) == 0 {
		return global
	}

	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.ToLower(strings.TrimSpace(canonicalModelKey(model)))

	bestScore := 0
	best := global
	for _, rule := range rules {
		score := 0
		if rule.Provider != "" {
			if rule.Provider != provider {
				continue
			}
			score += 2
		}
		if rule.Model != "" {
			if rule.Model != model {
				continue
			}
			score += 4
		}
		if score == 0 {
			continue
		}
		if score > bestScore {
			bestScore = score
			best = rule.MaxRequests
		}
	}
	return best
}

func sessionAffinityMaxRequestsExceeded(maxRequests, hits int) bool {
	return maxRequests > 0 && hits > maxRequests
}
