package registry

import (
	"strings"
	"time"
)

// AutoRouterProvider is the synthetic provider key under which Auto Router
// model ids are registered so they surface in /v1/models, the Models Group
// picker, and the Models Catalog. There is no real auth client behind this
// provider — the Auto Router resolves the request at execution time via the
// auto-router resolver.
const AutoRouterProvider = "auto-router"

// autoRouterClientPrefix prefixes the synthetic registry client id so it can
// never collide with an auth client id, and so the router can be unregistered
// by id without tracking a separate client id.
const autoRouterClientPrefix = "autorouter:"

// AutoRouterModelInfo builds a ModelInfo that exposes an Auto Router's
// requestable model_id as a normal, always-available model. OwnedBy/Type are
// set to the synthetic AutoRouterProvider so it is grouped under that provider
// in /v1/models and the catalog, and is never probed by health checks (see the
// health runner's auto-router exclusion). Created is optional (0 = no override).
func AutoRouterModelInfo(modelID, displayName string, created int64) *ModelInfo {
	display := strings.TrimSpace(displayName)
	if display == "" {
		display = modelID
	}
	info := &ModelInfo{
		ID:          strings.TrimSpace(modelID),
		Object:      "model",
		OwnedBy:     AutoRouterProvider,
		Type:        AutoRouterProvider,
		DisplayName: display,
		// Marked user_defined so periodic catalog auto-syncs (which upsert
		// with protectUserDefined=true) never clobber or delete the row.
		UserDefined: true,
	}
	if created > 0 {
		info.Created = created
	} else {
		info.Created = time.Now().Unix()
	}
	return info
}

// RegisterAutoRouterInRegistry registers an Auto Router's model id in the
// in-memory global registry under the synthetic auto-router provider, so it
// becomes "available" (visible in /v1/models, Models Group picker, and the
// Models Catalog live view). Calling it for an already-registered id is safe
// (RegisterClient reconciles counts/ids). A no-op when the model id is blank.
func RegisterAutoRouterInRegistry(modelID, displayName string, created int64) {
	if strings.TrimSpace(modelID) == "" {
		return
	}
	GetGlobalRegistry().RegisterClient(
		autoRouterClientPrefix+strings.TrimSpace(modelID),
		AutoRouterProvider,
		[]*ModelInfo{AutoRouterModelInfo(modelID, displayName, created)},
	)
}

// UnregisterAutoRouterFromRegistry removes an Auto Router's model id from the
// in-memory global registry. A no-op for blank ids and for ids never
// registered.
func UnregisterAutoRouterFromRegistry(modelID string) {
	if strings.TrimSpace(modelID) == "" {
		return
	}
	GetGlobalRegistry().UnregisterClient(autoRouterClientPrefix + strings.TrimSpace(modelID))
}

// IsAutoRouterModel reports whether a model id is served by the synthetic
// auto-router provider (i.e. was registered by an Auto Router). Used by the
// health sweep to skip probes against synthetic models that have no real auth
// client behind them.
func IsAutoRouterModel(modelID string) bool {
	if strings.TrimSpace(modelID) == "" {
		return false
	}
	for _, p := range GetGlobalRegistry().GetModelProviders(modelID) {
		if strings.EqualFold(p, AutoRouterProvider) {
			return true
		}
	}
	return false
}
