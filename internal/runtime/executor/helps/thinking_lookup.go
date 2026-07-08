package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func ThinkingLookupModelForAuth(auth *cliproxyauth.Auth, providerKey string, upstreamModel string, opts cliproxyexecutor.Options) string {
	if !cliproxyauth.IsConfigProviderAuth(auth) {
		return upstreamModel
	}
	lookupModel := PayloadThinkingLookupModel(opts, upstreamModel)
	if providerHasLookupModel(providerKey, lookupModel) {
		return lookupModel
	}
	return upstreamModel
}

func PayloadThinkingLookupModel(opts cliproxyexecutor.Options, fallback string) string {
	if len(opts.Metadata) == 0 {
		return fallback
	}
	raw, ok := opts.Metadata[cliproxyexecutor.ThinkingLookupModelMetadataKey]
	if !ok || raw == nil {
		return fallback
	}
	switch value := raw.(type) {
	case string:
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	case []byte:
		if trimmed := strings.TrimSpace(string(value)); trimmed != "" {
			return trimmed
		}
	}
	return fallback
}

func providerHasLookupModel(providerKey string, model string) bool {
	providerKey = strings.TrimSpace(providerKey)
	if providerKey == "" {
		return false
	}
	modelID := strings.TrimSpace(thinking.ParseSuffix(model).ModelName)
	if modelID == "" {
		return false
	}
	for _, provider := range registry.GetGlobalRegistry().GetModelProviders(modelID) {
		if strings.EqualFold(provider, providerKey) {
			return true
		}
	}
	return false
}
