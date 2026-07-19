package store

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// PGModelsAdapter adapts *ModelsStore (which speaks store.StoredModel) to the
// registry.ModelStoreOperator interface (which speaks *registry.ModelInfo).
// This keeps the import direction one-way: store imports registry, not vice
// versa, breaking the cycle that would otherwise arise from registry
// importing store directly.
type PGModelsAdapter struct {
	inner *ModelsStore
}

// NewPGModelsAdapter wraps a ModelsStore so it can be consumed by
// registry.PGSync as a ModelStoreOperator.
func NewPGModelsAdapter(inner *ModelsStore) *PGModelsAdapter {
	if inner == nil {
		return nil
	}
	return &PGModelsAdapter{inner: inner}
}

// Compile-time assertion that PGModelsAdapter satisfies the operator contract.
var _ registry.ModelStoreOperator = (*PGModelsAdapter)(nil)

// UpsertModels converts the supplied registry.ModelInfo slice into the
// store-layer wire form and delegates to the underlying ModelsStore.
func (a *PGModelsAdapter) UpsertModels(ctx context.Context, models []*registry.ModelInfo) error {
	if a == nil || a.inner == nil {
		return nil
	}
	if len(models) == 0 {
		return nil
	}
	stored := make([]StoredModel, 0, len(models))
	for _, m := range models {
		if m == nil || m.ID == "" {
			continue
		}
		stored = append(stored, ToStoredModel(m))
	}
	return a.inner.UpsertModels(ctx, stored)
}

// SelectAllModels returns the persisted catalog as registry.ModelInfo values.
func (a *PGModelsAdapter) SelectAllModels(ctx context.Context) ([]*registry.ModelInfo, error) {
	if a == nil || a.inner == nil {
		return nil, nil
	}
	stored, err := a.inner.SelectAll(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*registry.ModelInfo, 0, len(stored))
	for i := range stored {
		out = append(out, FromStoredModel(&stored[i]))
	}
	return out, nil
}

// DeleteModelsByProvider removes all rows for the given provider.
func (a *PGModelsAdapter) DeleteModelsByProvider(ctx context.Context, provider string) (int64, error) {
	if a == nil || a.inner == nil {
		return 0, nil
	}
	return a.inner.DeleteByProvider(ctx, provider)
}

// CountModels returns the total catalog row count.
func (a *PGModelsAdapter) CountModels(ctx context.Context) (int64, error) {
	if a == nil || a.inner == nil {
		return 0, nil
	}
	return a.inner.Count(ctx)
}

// ToStoredModel converts a registry.ModelInfo into the store-layer wire form.
// Provider is auto-derived from OwnedBy → Type when not set explicitly.
func ToStoredModel(m *registry.ModelInfo) StoredModel {
	provider := m.OwnedBy
	if provider == "" {
		provider = m.Type
	}
	out := StoredModel{
		ID:                         m.ID,
		Provider:                   provider,
		Object:                     m.Object,
		Created:                    m.Created,
		OwnedBy:                    m.OwnedBy,
		Type:                       m.Type,
		DisplayName:                m.DisplayName,
		Name:                       m.Name,
		Version:                    m.Version,
		Description:                m.Description,
		InputTokenLimit:            m.InputTokenLimit,
		OutputTokenLimit:           m.OutputTokenLimit,
		SupportedGenerationMethods: append([]string(nil), m.SupportedGenerationMethods...),
		ContextLength:              m.ContextLength,
		MaxCompletionTokens:        m.MaxCompletionTokens,
		SupportedParameters:        append([]string(nil), m.SupportedParameters...),
		InputModalities:            append([]string(nil), m.SupportedInputModalities...),
		OutputModalities:           append([]string(nil), m.SupportedOutputModalities...),
		SupportsWebSearch:          m.SupportsWebSearch,
		UserDefined:                m.UserDefined,
	}
	if m.Thinking != nil {
		out.Thinking = map[string]any{
			"min":             m.Thinking.Min,
			"max":             m.Thinking.Max,
			"zero_allowed":    m.Thinking.ZeroAllowed,
			"dynamic_allowed": m.Thinking.DynamicAllowed,
			"levels":          append([]string(nil), m.Thinking.Levels...),
		}
	}
	if m.Config != nil && len(m.Config.OverrideHeader) > 0 {
		out.OverrideHeader = make(map[string]string, len(m.Config.OverrideHeader))
		for k, v := range m.Config.OverrideHeader {
			out.OverrideHeader[k] = v
		}
	}
	return out
}

// FromStoredModel performs the inverse conversion: PG row → registry.ModelInfo.
func FromStoredModel(m *StoredModel) *registry.ModelInfo {
	out := &registry.ModelInfo{
		ID:                         m.ID,
		Object:                     m.Object,
		Created:                    m.Created,
		OwnedBy:                    m.OwnedBy,
		Type:                       m.Type,
		DisplayName:                m.DisplayName,
		Name:                       m.Name,
		Version:                    m.Version,
		Description:                m.Description,
		InputTokenLimit:            m.InputTokenLimit,
		OutputTokenLimit:           m.OutputTokenLimit,
		SupportedGenerationMethods: append([]string(nil), m.SupportedGenerationMethods...),
		ContextLength:              m.ContextLength,
		MaxCompletionTokens:        m.MaxCompletionTokens,
		SupportedParameters:        append([]string(nil), m.SupportedParameters...),
		SupportedInputModalities:   append([]string(nil), m.InputModalities...),
		SupportedOutputModalities:  append([]string(nil), m.OutputModalities...),
		SupportsWebSearch:          m.SupportsWebSearch,
		UserDefined:                m.UserDefined,
	}
	if len(m.Thinking) > 0 {
		t := &registry.ThinkingSupport{}
		if v, ok := m.Thinking["min"].(float64); ok {
			t.Min = int(v)
		}
		if v, ok := m.Thinking["max"].(float64); ok {
			t.Max = int(v)
		}
		if v, ok := m.Thinking["zero_allowed"].(bool); ok {
			t.ZeroAllowed = v
		}
		if v, ok := m.Thinking["dynamic_allowed"].(bool); ok {
			t.DynamicAllowed = v
		}
		if levels, ok := m.Thinking["levels"].([]any); ok {
			t.Levels = make([]string, 0, len(levels))
			for _, lv := range levels {
				if s, ok := lv.(string); ok {
					t.Levels = append(t.Levels, s)
				}
			}
		}
		out.Thinking = t
	}
	if len(m.OverrideHeader) > 0 {
		out.Config = &registry.ModelConfig{OverrideHeader: make(map[string]string, len(m.OverrideHeader))}
		for k, v := range m.OverrideHeader {
			out.Config.OverrideHeader[k] = v
		}
	}
	return out
}
