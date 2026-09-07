package store

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter"
)

// AutoRoutersResolverImpl adapts AutoRouterStore and the optional profile store
// to the request-time Auto Router resolver contract.
type AutoRoutersResolverImpl struct {
	store    *AutoRouterStore
	profiles *AutoRouterProfileStore
}

// NewAutoRoutersResolver wraps an AutoRouterStore. The optional profile store
// enables versioned scoring policies; omitting it preserves built-in defaults.
func NewAutoRoutersResolver(store *AutoRouterStore, profiles ...*AutoRouterProfileStore) *AutoRoutersResolverImpl {
	if store == nil {
		return nil
	}
	var profileStore *AutoRouterProfileStore
	if len(profiles) > 0 {
		profileStore = profiles[0]
	}
	return &AutoRoutersResolverImpl{store: store, profiles: profileStore}
}

// AutoRouterForModel returns the Auto Router exposing the supplied client-facing
// model id, or nil when no router owns it.
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
	cp := router
	return &cp
}

// AutoRouterProfile returns the active profile for a persisted router id. A
// missing profile is represented by the built-in default profile.
func (r *AutoRoutersResolverImpl) AutoRouterProfile(ctx context.Context, routerID string) *autorouter.Profile {
	if r == nil || ctx == nil {
		return nil
	}
	if r.profiles == nil {
		profile := autorouter.DefaultProfile()
		return &profile
	}
	profile, err := r.profiles.Get(ctx, strings.TrimSpace(routerID))
	if err != nil {
		profile := autorouter.DefaultProfile()
		return &profile
	}
	return &profile
}

// AutoRouterProfileCompiled returns the compiled scoring profile for a router
// id, falling back to the built-in compiled defaults on any error. Compiled
// profiles skip all per-request normalization work.
func (r *AutoRoutersResolverImpl) AutoRouterProfileCompiled(ctx context.Context, routerID string) *autorouter.CompiledProfile {
	if r == nil || ctx == nil {
		def := autorouter.DefaultCompiledProfile()
		return &def
	}
	if r.profiles == nil {
		def := autorouter.DefaultCompiledProfile()
		return &def
	}
	compiled, err := r.profiles.Compiled(ctx, strings.TrimSpace(routerID))
	if err != nil || compiled == nil {
		def := autorouter.DefaultCompiledProfile()
		return &def
	}
	return compiled
}

var _ interface {
	AutoRouterForModel(context.Context, string) *AutoRouter
	AutoRouterProfile(context.Context, string) *autorouter.Profile
	AutoRouterProfileCompiled(context.Context, string) *autorouter.CompiledProfile
} = (*AutoRoutersResolverImpl)(nil)
