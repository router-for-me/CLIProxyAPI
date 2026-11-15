package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type allowedAuthsContextKey struct{}

// AllowedAuthsGinKey is the gin context key under which access middleware stores
// the credential allow-list. Handlers derive fresh contexts that keep the gin
// context under "gin", so the selector also looks the list up there.
const AllowedAuthsGinKey = "cliproxy.allowedAuths"

// NewAllowedAuthSet normalizes an allow-list into a lookup set; it returns nil when empty.
func NewAllowedAuthSet(allowed []string) map[string]struct{} {
	set := make(map[string]struct{}, len(allowed))
	for _, entry := range allowed {
		if entry = strings.TrimSpace(entry); entry != "" {
			set[entry] = struct{}{}
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

// WithAllowedAuths restricts credential selection for the request to the given
// credentials. Entries may be auth indexes (auth_index) or credential IDs.
// An empty list leaves selection unrestricted.
func WithAllowedAuths(ctx context.Context, allowed []string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	set := NewAllowedAuthSet(allowed)
	if set == nil {
		return ctx
	}
	return context.WithValue(ctx, allowedAuthsContextKey{}, set)
}

// AllowedAuthsFromContext returns the credential allow-list attached to ctx, or nil.
func AllowedAuthsFromContext(ctx context.Context) map[string]struct{} {
	if ctx == nil {
		return nil
	}
	if set, ok := ctx.Value(allowedAuthsContextKey{}).(map[string]struct{}); ok && set != nil {
		return set
	}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil {
		if raw, exists := ginCtx.Get(AllowedAuthsGinKey); exists {
			if set, okSet := raw.(map[string]struct{}); okSet && set != nil {
				return set
			}
		}
	}
	return nil
}

// AuthAllowedBy reports whether auth matches the allow-list. A nil list allows everything.
func AuthAllowedBy(allowed map[string]struct{}, auth *Auth) bool {
	if allowed == nil {
		return true
	}
	if auth == nil {
		return false
	}
	if _, ok := allowed[auth.ID]; ok {
		return true
	}
	index := strings.TrimSpace(auth.Index)
	if index == "" {
		// Do not mutate shared auth state from the selection path.
		if seed := auth.indexSeed(); seed != "" {
			index = stableAuthIndex(seed)
		}
	}
	if index == "" {
		return false
	}
	_, ok := allowed[index]
	return ok
}

// AllowedModelsGinKey is the gin context key holding a client key's model patterns.
const AllowedModelsGinKey = "cliproxy.allowedModels"

type allowedModelsContextKey struct{}

// WithAllowedModels restricts the request to models matching the given patterns
// ("*" matches any run of characters). An empty list leaves models unrestricted.
func WithAllowedModels(ctx context.Context, patterns []string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(patterns) == 0 {
		return ctx
	}
	return context.WithValue(ctx, allowedModelsContextKey{}, append([]string(nil), patterns...))
}

// AllowedModelsFromContext returns the model patterns attached to ctx, or nil.
func AllowedModelsFromContext(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	if patterns, ok := ctx.Value(allowedModelsContextKey{}).([]string); ok && len(patterns) > 0 {
		return patterns
	}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil {
		if raw, exists := ginCtx.Get(AllowedModelsGinKey); exists {
			if patterns, okList := raw.([]string); okList && len(patterns) > 0 {
				return patterns
			}
		}
	}
	return nil
}

// ModelAllowedBy reports whether any model name matches the patterns. Nil patterns allow all.
func ModelAllowedBy(patterns []string, models ...string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, model := range models {
		for _, pattern := range patterns {
			if internalconfig.MatchModelPattern(pattern, model) {
				return true
			}
		}
	}
	return false
}

// checkAllowedModel rejects requests for models outside the client key's allow-list.
// Both the client-requested name and the resolved route model are accepted.
func checkAllowedModel(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
	patterns := AllowedModelsFromContext(ctx)
	if len(patterns) == 0 {
		return nil
	}
	candidates := []string{req.Model}
	if requested, ok := opts.Metadata[cliproxyexecutor.RequestedModelMetadataKey].(string); ok && requested != "" {
		candidates = append(candidates, requested)
		if _, base, found := strings.Cut(requested, "/"); found {
			candidates = append(candidates, base)
		}
	}
	if ModelAllowedBy(patterns, candidates...) {
		return nil
	}
	name := req.Model
	if requested, ok := opts.Metadata[cliproxyexecutor.RequestedModelMetadataKey].(string); ok && requested != "" {
		name = requested
	}
	return &Error{Code: "model_not_allowed", Message: "this API key is not allowed to use model " + name, HTTPStatus: http.StatusForbidden}
}
