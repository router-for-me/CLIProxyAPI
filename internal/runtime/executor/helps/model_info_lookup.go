package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// ModelInfoLookup carries the model id and registry entry that should be used
// for request-time model capability checks.
type ModelInfoLookup struct {
	Model    string
	Info     *registry.ModelInfo
	Resolved bool
}

// RequestModelInfo resolves the ModelInfo for a specific execution attempt.
//
// Providers can rewrite a prefixed client model into an unprefixed upstream
// model before executors run. In that case the registry capability entry may
// belong to the prefixed model, so prefer a request-scoped lookup candidate
// only when that exact model is registered for the current provider.
func RequestModelInfo(auth *cliproxyauth.Auth, providerKey string, upstreamModel string, opts cliproxyexecutor.Options) ModelInfoLookup {
	providerKey = strings.TrimSpace(providerKey)
	upstreamModel = strings.TrimSpace(upstreamModel)
	lookupModel := upstreamModel

	for _, candidate := range modelInfoLookupCandidates(auth, opts, upstreamModel) {
		if providerHasModel(providerKey, candidate) {
			lookupModel = candidate
			break
		}
	}

	info := lookupModelInfo(providerKey, lookupModel)
	if !sameModelBase(lookupModel, upstreamModel) {
		upstreamInfo := lookupModelInfo(providerKey, upstreamModel)
		if info == nil {
			info = upstreamInfo
		} else if info.Thinking == nil && !info.ThinkingExplicit && upstreamInfo != nil {
			info.Thinking = upstreamInfo.Thinking
		}
	}
	return ModelInfoLookup{Model: lookupModel, Info: info, Resolved: true}
}

func modelInfoLookupCandidates(auth *cliproxyauth.Auth, opts cliproxyexecutor.Options, upstreamModel string) []string {
	candidates := make([]string, 0, 3)
	if candidate := PayloadModelInfoLookupModel(opts, ""); strings.TrimSpace(candidate) != "" {
		candidates = append(candidates, candidate)
	}
	if auth != nil {
		prefix := strings.Trim(strings.TrimSpace(auth.Prefix), "/")
		suffix := thinking.ParseSuffix(upstreamModel)
		base := strings.TrimSpace(suffix.ModelName)
		if prefix != "" && base != "" && !hasModelPrefix(base, prefix) {
			candidate := prefix + "/" + base
			if suffix.HasSuffix && strings.TrimSpace(suffix.RawSuffix) != "" {
				candidate += "(" + strings.TrimSpace(suffix.RawSuffix) + ")"
			}
			candidates = append(candidates, candidate)
		}
	}
	if strings.TrimSpace(upstreamModel) != "" {
		candidates = append(candidates, upstreamModel)
	}
	return candidates
}

// ApplyRequestThinking applies thinking using the request-scoped ModelInfo
// lookup for config API-key providers.
func ApplyRequestThinking(body []byte, auth *cliproxyauth.Auth, providerKey string, model string, opts cliproxyexecutor.Options, fromFormat string, toFormat string) ([]byte, error) {
	lookup := RequestModelInfo(auth, providerKey, model, opts)
	return thinking.ApplyThinkingWithResolvedModelInfo(body, nil, lookup.Model, fromFormat, toFormat, providerKey, lookup.Info, lookup.Resolved)
}

// ApplyRequestThinkingWithSource applies thinking to a translated request body
// while preserving source-format thinking parameters for request-scoped model
// capability checks.
func ApplyRequestThinkingWithSource(body []byte, sourceBody []byte, auth *cliproxyauth.Auth, providerKey string, model string, opts cliproxyexecutor.Options, fromFormat string, toFormat string) ([]byte, error) {
	lookup := RequestModelInfo(auth, providerKey, model, opts)
	return thinking.ApplyThinkingWithResolvedModelInfo(body, sourceBody, lookup.Model, fromFormat, toFormat, providerKey, lookup.Info, lookup.Resolved)
}

// PayloadModelInfoLookupModel returns the per-attempt model capability lookup
// model, falling back to fallback when metadata is absent.
func PayloadModelInfoLookupModel(opts cliproxyexecutor.Options, fallback string) string {
	if len(opts.Metadata) == 0 {
		return fallback
	}
	raw, ok := opts.Metadata[cliproxyexecutor.ModelInfoLookupModelMetadataKey]
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

func lookupModelInfo(providerKey string, model string) *registry.ModelInfo {
	modelID := strings.TrimSpace(thinking.ParseSuffix(model).ModelName)
	if modelID == "" {
		return nil
	}
	if strings.TrimSpace(providerKey) != "" {
		providers := registry.GetGlobalRegistry().GetModelProviders(modelID)
		if len(providers) > 0 && !providerInList(providers, providerKey) {
			return registry.LookupStaticModelInfo(modelID)
		}
	}
	return registry.LookupModelInfo(modelID, providerKey)
}

func providerHasModel(providerKey string, model string) bool {
	providerKey = strings.TrimSpace(providerKey)
	if providerKey == "" {
		return false
	}
	modelID := strings.TrimSpace(thinking.ParseSuffix(model).ModelName)
	if modelID == "" {
		return false
	}
	return providerInList(registry.GetGlobalRegistry().GetModelProviders(modelID), providerKey)
}

func providerInList(providers []string, providerKey string) bool {
	providerKey = strings.TrimSpace(providerKey)
	if providerKey == "" {
		return false
	}
	for _, provider := range providers {
		if strings.EqualFold(provider, providerKey) {
			return true
		}
	}
	return false
}

func sameModelBase(a, b string) bool {
	a = strings.TrimSpace(thinking.ParseSuffix(a).ModelName)
	b = strings.TrimSpace(thinking.ParseSuffix(b).ModelName)
	return strings.EqualFold(a, b)
}

func hasModelPrefix(model string, prefix string) bool {
	model = strings.TrimSpace(model)
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if model == "" || prefix == "" {
		return false
	}
	return strings.HasPrefix(strings.ToLower(model), strings.ToLower(prefix)+"/")
}
