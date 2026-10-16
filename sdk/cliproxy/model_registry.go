package cliproxy

import (
	"maps"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
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
// registry epoch observed when a registration began. At the publication
// boundary, the manager identity gate runs first under the auth's mutation
// lock, then the concrete registry performs an atomic epoch CAS. This keeps
// the lock order consistently auth-mutation before registry and prevents an
// older computation from overwriting or removing a newer publication.
type modelRegistrationFence struct {
	registryEpoch     uint64
	registrationEpoch uint64
	prefix            string
	provider          string
	disabled          bool
	authKind          string
	attributes        map[string]string
}

func newModelRegistrationFence(a *coreauth.Auth) modelRegistrationFence {
	if a == nil {
		return modelRegistrationFence{}
	}
	return modelRegistrationFence{
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
// current values. The registry epoch intentionally remains the starting
// epoch so a newer intervening publication cannot be adopted by this fence.
func (f modelRegistrationFence) withAuth(a *coreauth.Auth) modelRegistrationFence {
	if a == nil {
		return f
	}
	next := newModelRegistrationFence(a)
	next.registryEpoch = f.registryEpoch
	return next
}

func (f modelRegistrationFence) publishModels(manager *coreauth.Manager, authID, providerKey string, models []*ModelInfo) bool {
	publish := func() bool {
		return GlobalModelRegistry().RegisterClientIfEpochCurrent(authID, providerKey, f.registryEpoch, models, func() bool { return true })
	}
	if manager == nil {
		return publish()
	}
	return manager.PublishIfRegistrationCurrent(authID, f.registrationEpoch, f.prefix, f.provider, f.disabled, f.authKind, f.attributes, publish)
}

func (f modelRegistrationFence) publishUnregister(manager *coreauth.Manager, authID string) bool {
	publish := func() bool {
		return GlobalModelRegistry().UnregisterClientIfEpochCurrent(authID, f.registryEpoch, func() bool { return true })
	}
	if manager == nil {
		return publish()
	}
	return manager.PublishIfRegistrationCurrent(authID, f.registrationEpoch, f.prefix, f.provider, f.disabled, f.authKind, f.attributes, publish)
}
