package auth

import (
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

// RouteModelPreview describes a selectable route model and the upstream models
// it would resolve to without mutating scheduler state.
type RouteModelPreview struct {
	Auth           *Auth
	RouteModel     string
	CreatedAt      int64
	UpstreamModels []string
}

// ExecutionModelCandidates resolves the upstream model candidates for a route model
// under a specific auth without advancing model-pool rotation state.
func (m *Manager) ExecutionModelCandidates(auth *Auth, routeModel string) []string {
	if m == nil {
		return nil
	}
	return m.resolveExecutionModels(auth, routeModel, false)
}

// PreviewSelectableRouteModels lists currently selectable route models together
// with their upstream execution-model previews.
func (m *Manager) PreviewSelectableRouteModels(now time.Time, authAllowed func(*Auth) bool) []RouteModelPreview {
	if m == nil {
		return nil
	}
	reg := registry.GetGlobalRegistry()
	previews := make([]RouteModelPreview, 0)
	for _, auth := range m.List() {
		if auth == nil || strings.TrimSpace(auth.ID) == "" {
			continue
		}
		if authAllowed != nil && !authAllowed(auth) {
			continue
		}
		for _, modelInfo := range reg.GetModelsForClient(auth.ID) {
			if modelInfo == nil {
				continue
			}
			routeModel := strings.TrimSpace(modelInfo.ID)
			if routeModel == "" {
				continue
			}
			if !IsAuthSelectableForModel(auth, routeModel, now) {
				continue
			}
			upstreamModels := m.ExecutionModelCandidates(auth, routeModel)
			if len(upstreamModels) == 0 {
				continue
			}
			previews = append(previews, RouteModelPreview{
				Auth:           auth,
				RouteModel:     routeModel,
				CreatedAt:      modelInfo.Created,
				UpstreamModels: append([]string(nil), upstreamModels...),
			})
		}
	}
	sort.SliceStable(previews, func(i, j int) bool {
		if previews[i].Auth != nil && previews[j].Auth != nil && previews[i].Auth.ID != previews[j].Auth.ID {
			return previews[i].Auth.ID < previews[j].Auth.ID
		}
		return previews[i].RouteModel < previews[j].RouteModel
	})
	return previews
}

func (m *Manager) resolveExecutionModels(auth *Auth, routeModel string, rotatePool bool) []string {
	requestedModel := rewriteModelForAuth(routeModel, auth)
	requestedModel = m.applyOAuthModelAlias(auth, requestedModel)
	if pool := m.resolveOpenAICompatUpstreamModelPool(auth, requestedModel); len(pool) > 0 {
		if len(pool) == 1 {
			return append([]string(nil), pool...)
		}
		if rotatePool {
			offset := m.nextModelPoolOffset(openAICompatModelPoolKey(auth, requestedModel), len(pool))
			return rotateStrings(pool, offset)
		}
		return rotateStrings(pool, 0)
	}
	resolved := m.applyAPIKeyModelAlias(auth, requestedModel)
	if strings.TrimSpace(resolved) == "" {
		resolved = requestedModel
	}
	return []string{resolved}
}
