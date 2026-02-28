package fallback

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
)

// Plan generates the ordered fallback chain for a requested model.
// The chain always starts with the requested model itself, followed by fallback candidates.
// It handles thinking suffix preservation: if the requested model has a suffix like (8192),
// the suffix is carried over to fallback candidates that don't already have one.
func Plan(requestedModel string, cfg *config.Config) []string {
	if cfg == nil || !cfg.ModelFallback.Enabled {
		return []string{requestedModel}
	}
	fb := cfg.ModelFallback

	// Parse thinking suffix from requested model
	suffix := thinking.ParseSuffix(requestedModel)
	baseModel := suffix.ModelName

	// Look up fallback candidates
	candidates := lookupCandidates(baseModel, fb)
	if len(candidates) == 0 {
		return []string{requestedModel}
	}

	// Build chain: original model + candidates (up to max attempts)
	maxAttempts := MaxAttempts(cfg, baseModel)
	chain := make([]string, 0, maxAttempts)
	seen := make(map[string]struct{})

	// Always start with the original requested model
	chain = append(chain, requestedModel)
	seen[strings.ToLower(requestedModel)] = struct{}{}

	// Add candidates with suffix preservation
	for _, candidate := range candidates {
		if len(chain) >= maxAttempts {
			break
		}
		normalizedCandidate := applySuffix(candidate, suffix)
		key := strings.ToLower(normalizedCandidate)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		chain = append(chain, normalizedCandidate)
	}

	return chain
}

// lookupCandidates finds fallback candidates from rules and model overrides.
// Override keys are normalized to lowercase by SanitizeModelFallback; lookup uses ToLower for safety.
func lookupCandidates(baseModel string, fb config.ModelFallback) []string {
	// Check model-specific override first (case-insensitive via normalized key)
	if override, ok := fb.ModelOverrides[strings.ToLower(baseModel)]; ok && len(override.To) > 0 {
		return override.To
	}

	// Check rules
	for _, rule := range fb.Rules {
		if strings.EqualFold(rule.From, baseModel) {
			return rule.To
		}
	}

	return nil
}

// applySuffix applies the original thinking suffix to a candidate model.
// If the candidate already has a suffix, it's left unchanged.
func applySuffix(candidate string, origSuffix thinking.SuffixResult) string {
	if !origSuffix.HasSuffix {
		return candidate
	}
	// Don't add suffix if candidate already has one
	candidateSuffix := thinking.ParseSuffix(candidate)
	if candidateSuffix.HasSuffix {
		return candidate
	}
	return candidate + "(" + origSuffix.RawSuffix + ")"
}
