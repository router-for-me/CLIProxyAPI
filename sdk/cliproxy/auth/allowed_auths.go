package auth

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
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
