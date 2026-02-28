package fallback

import (
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

// FilterProviders returns providers for the attempt model that are allowed
// by the cross-provider policy. If the original model and attempt model share
// a provider, all providers for the attempt model are returned.
// If cross-provider is not allowed, only overlapping providers are returned.
func FilterProviders(requestedModel, attemptModel string, cfg *config.Config) []string {
	attemptProviders := util.GetProviderName(attemptModel)
	if len(attemptProviders) == 0 {
		return nil
	}

	// Same model — no filtering needed
	if requestedModel == attemptModel {
		return attemptProviders
	}

	if cfg == nil {
		return attemptProviders
	}
	fb := cfg.ModelFallback

	// If cross-provider is allowed, return all providers for the attempt model
	if fb.AllowCrossProvider {
		return attemptProviders
	}

	// Get providers for the original requested model
	requestedProviders := util.GetProviderName(requestedModel)
	if len(requestedProviders) == 0 {
		// Can't determine original provider — allow attempt as fallback
		return attemptProviders
	}

	// Build set of original providers
	origSet := make(map[string]struct{}, len(requestedProviders))
	for _, p := range requestedProviders {
		origSet[p] = struct{}{}
	}

	// Filter to only providers that overlap with original model's providers
	var filtered []string
	for _, p := range attemptProviders {
		if _, ok := origSet[p]; ok {
			filtered = append(filtered, p)
		}
	}

	return filtered
}
