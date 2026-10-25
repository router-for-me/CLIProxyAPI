package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// AntigravityUpstreamModel uses the selected account's fenced model publication.
// Registry routing is available before asynchronous hint persistence completes.
func AntigravityUpstreamModel(auth *cliproxyauth.Auth, modelName string) string {
	if auth != nil && auth.ID != "" {
		for _, model := range registry.GetGlobalRegistry().GetModelsForClient(auth.ID) {
			if model != nil && model.UpstreamModelID != "" &&
				(strings.EqualFold(model.ID, modelName) || strings.EqualFold(model.MetadataModelID, modelName)) {
				return model.UpstreamModelID
			}
		}
	}
	return registry.ResolveAntigravityUpstreamModel(modelName, nil)
}
