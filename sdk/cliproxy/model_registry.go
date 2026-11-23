package cliproxy

import (
	"maps"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/diff"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// ModelInfo re-exports the registry model info structure.
type ModelInfo = registry.ModelInfo

// ClientModelProjection re-exports the registry model projection structure.
type ClientModelProjection = registry.ClientModelProjection

// ModelRegistryHook re-exports the registry hook interface for external integrations.
type ModelRegistryHook = registry.ModelRegistryHook

// ModelRegistry describes registry operations consumed by external callers.
type ModelRegistry interface {
	RegisterClient(clientID, clientProvider string, models []*ModelInfo)
	RegisterClientIfEpochCurrent(clientID, clientProvider string, expectedEpoch uint64, models []*ModelInfo, publish func() bool) bool
	UnregisterClient(clientID string)
	UnregisterClientIfEpochCurrent(clientID string, expectedEpoch uint64, publish func() bool) bool
	SetModelQuotaExceeded(clientID, modelID string)
	ClearModelQuotaExceeded(clientID, modelID string)
	ApplyClientModelProjections(clientID string, epoch uint64, generation uint64, projections []ClientModelProjection) bool
	ClientSupportsModel(clientID, modelID string) bool
	GetAvailableModels(handlerType string) []map[string]any
	GetAvailableModelsByProvider(provider string) []*ModelInfo
	GetModelsForClient(clientID string) []*ModelInfo
	GetModelsAndEpochForClient(clientID string) ([]*ModelInfo, uint64)
	ClientRegistrationEpoch(clientID string) uint64
	ApplyClientModelCapabilities(clientID string, expectedEpoch uint64, mutate func(modelID string, info *ModelInfo)) bool
}

// GlobalModelRegistry returns the shared registry instance.
func GlobalModelRegistry() ModelRegistry {
	return registry.GetGlobalRegistry()
}

// SetGlobalModelRegistryHook registers an optional hook on the shared global registry instance.
func SetGlobalModelRegistryHook(hook ModelRegistryHook) {
	registry.GetGlobalRegistry().SetHook(hook)
}

// modelRegistrationFence captures the model-registration inputs and the
// global exclusion summaries and registry epoch observed when registration began.
// Publication takes the auth mutation lock, then the config read lock, then the
// registry lock. Obsolete inherited exclusions must not advance the registry epoch
// and invalidate newer work, even if the credential attributes are unchanged.
type modelRegistrationFence struct {
	globalExclusions          map[string]diff.ExcludedModelsSummary
	exclusionProvider         string
	fallbackExclusionProvider string
	registryEpoch             uint64
	registrationEpoch         uint64
	prefix                    string
	provider                  string
	disabled                  bool
	authKind                  string
	attributes                map[string]string
}

func newModelRegistrationFence(a *coreauth.Auth, cfg *config.Config) modelRegistrationFence {
	var exclusions map[string]diff.ExcludedModelsSummary
	if cfg != nil && len(cfg.OAuthExcludedModels) > 0 {
		exclusions = make(map[string]diff.ExcludedModelsSummary, len(cfg.OAuthExcludedModels))
		for provider, rules := range cfg.OAuthExcludedModels {
			exclusions[provider] = diff.SummarizeExcludedModels(rules)
		}
	}
	if a == nil {
		return modelRegistrationFence{globalExclusions: exclusions}
	}
	provider := strings.ToLower(strings.TrimSpace(a.Provider))
	if _, _, compat := openAICompatInfoFromAuth(a); compat {
		provider = "openai-compatibility"
	}
	return modelRegistrationFence{
		globalExclusions:  exclusions,
		exclusionProvider: provider,
		registryEpoch:     GlobalModelRegistry().ClientRegistrationEpoch(a.ID),
		registrationEpoch: a.RegistrationEpoch,
		prefix:            a.Prefix,
		provider:          a.Provider,
		disabled:          a.Disabled || a.Status == coreauth.StatusDisabled,
		authKind:          a.AuthKind(),
		attributes:        maps.Clone(a.Attributes),
	}
}

// withAuth returns a copy of f with the auth-input fields replaced by a's
// current values. Exclusion summaries and the registry epoch intentionally remain
// unchanged so stale work cannot adopt newer global rules or publications.
func (f modelRegistrationFence) withAuth(a *coreauth.Auth) modelRegistrationFence {
	if a == nil {
		return f
	}
	next := newModelRegistrationFence(a, nil)
	next.globalExclusions = f.globalExclusions
	next.fallbackExclusionProvider = f.fallbackExclusionProvider
	next.registryEpoch = f.registryEpoch
	return next
}

func (f modelRegistrationFence) publishModels(s *Service, authID, providerKey string, models []*ModelInfo) bool {
	return f.publishIfCurrent(s, authID, func() bool {
		return GlobalModelRegistry().RegisterClientIfEpochCurrent(authID, providerKey, f.registryEpoch, models, func() bool { return true })
	})
}

func (f modelRegistrationFence) publishUnregister(s *Service, authID string) bool {
	return f.publishIfCurrent(s, authID, func() bool {
		return GlobalModelRegistry().UnregisterClientIfEpochCurrent(authID, f.registryEpoch, func() bool { return true })
	})
}

func (f modelRegistrationFence) publishIfCurrent(s *Service, authID string, publish func() bool) bool {
	publishWithConfig := func() bool {
		if s != nil && !f.disabled && f.authKind != coreauth.AuthKindAPIKey && strings.TrimSpace(f.attributes["excluded_models"]) == "" {
			s.cfgMu.RLock()
			defer s.cfgMu.RUnlock()
			// Match the watcher's normalization so no-op and unrelated reloads do not
			// discard work. A real change always schedules a replacement: the watcher
			// redispatches affected auths, and applyConfigRuntime re-registers every
			// auth (which also covers Home mode, where no watcher runs).
			for _, provider := range [2]string{f.exclusionProvider, f.fallbackExclusionProvider} {
				if provider == "" {
					continue
				}
				current := oauthExcludedModelsForConfig(s.cfg, provider, f.authKind)
				if f.globalExclusions[provider] != diff.SummarizeExcludedModels(current) {
					return false
				}
			}
			// Hold cfgMu through registry publication, not just the comparison.
		}
		return publish()
	}
	if s == nil || s.coreManager == nil {
		return publishWithConfig()
	}
	return s.coreManager.PublishIfRegistrationCurrent(authID, f.registrationEpoch, f.prefix, f.provider, f.disabled, f.authKind, f.attributes, publishWithConfig)
}
