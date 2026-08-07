package store

import (
	"context"
	"strings"
)

// AutoRoutersResolverImpl adapts *AutoRouterStore to the
// handlers.AutoRouterResolver contract: given a requested model id, return the
// matching Auto Router definition (or nil when the model id does not belong to
// any router). It implements AutoRouterForModel(ctx, modelID) *AutoRouter
// without importing the handlers package (the interface is satisfied
// structurally, mirroring ModelsCatalogResolverImpl).
//
// The store serves results from its in-memory per-model cache, so request-time
// reads hit the DB at most once per model id.
type AutoRoutersResolverImpl struct {
	store *AutoRouterStore
}

// NewAutoRoutersResolver wraps an AutoRouterStore. Returns nil when the store
// is nil (file-based deployments) so callers can short-circuit.
func NewAutoRoutersResolver(store *AutoRouterStore) *AutoRoutersResolverImpl {
	if store == nil {
		return nil
	}
	return &AutoRoutersResolverImpl{store: store}
}

// AutoRouterForModel returns the Auto Router exposing the supplied client-facing
// model id, or nil when no router owns it. The returned pointer is a copy from
// the store cache, so the caller may not mutate it. An empty match (unknown or
// not-a-router model id) yields nil, signalling the caller to use normal
// model-to-provider routing.
func (r *AutoRoutersResolverImpl) AutoRouterForModel(ctx context.Context, modelID string) *AutoRouter {
	if r == nil || r.store == nil || ctx == nil {
		return nil
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return nil
	}
	router, err := r.store.GetByModelID(ctx, modelID)
	if err != nil {
		return nil
	}
	// Copy so the caller cannot mutate the cached object.
	cp := router
	return &cp
}
