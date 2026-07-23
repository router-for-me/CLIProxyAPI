// Package codexintegration integrates CLIProxyAPI's model registry and data plane with Codex.
package codexintegration

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// ModelPolicy resolves stable Codex-visible model slugs to fixed provider targets.
type ModelPolicy struct {
	models []config.CodexIntegrationModel
	bySlug map[string]config.CodexIntegrationModel
}

// NewModelPolicy builds an immutable stable model policy.
func NewModelPolicy(models []config.CodexIntegrationModel) (*ModelPolicy, error) {
	policy := &ModelPolicy{
		models: append([]config.CodexIntegrationModel(nil), models...),
		bySlug: make(map[string]config.CodexIntegrationModel, len(models)),
	}
	for i, model := range models {
		model.Slug = strings.ToLower(strings.TrimSpace(model.Slug))
		model.Provider = strings.ToLower(strings.TrimSpace(model.Provider))
		model.UpstreamModel = strings.TrimSpace(model.UpstreamModel)
		if model.Slug == "" || model.Provider == "" || model.UpstreamModel == "" {
			return nil, fmt.Errorf("models[%d]: slug, provider, and upstream model are required", i)
		}
		if !IsReservedProvider(model.Provider) || !strings.HasPrefix(model.Slug, model.Provider+"/") {
			return nil, fmt.Errorf("models[%d]: slug %q does not match reserved provider %q", i, model.Slug, model.Provider)
		}
		if _, exists := policy.bySlug[model.Slug]; exists {
			return nil, fmt.Errorf("models[%d]: duplicate slug %q", i, model.Slug)
		}
		policy.models[i] = model
		policy.bySlug[model.Slug] = model
	}
	return policy, nil
}

// Resolve returns the exact provider mapping for a stable slug.
func (p *ModelPolicy) Resolve(slug string) (config.CodexIntegrationModel, bool) {
	if p == nil {
		return config.CodexIntegrationModel{}, false
	}
	model, ok := p.bySlug[strings.ToLower(strings.TrimSpace(slug))]
	return model, ok
}

// Models returns an independent copy of the stable model policy.
func (p *ModelPolicy) Models() []config.CodexIntegrationModel {
	if p == nil {
		return nil
	}
	models := make([]config.CodexIntegrationModel, len(p.models))
	copy(models, p.models)
	for i := range models {
		models[i].InputModalities = append([]string(nil), models[i].InputModalities...)
	}
	return models
}

// IsReservedProvider reports whether a slash prefix is owned by Codex Integration.
func IsReservedProvider(provider string) bool {
	return config.IsCodexIntegrationProvider(provider)
}
