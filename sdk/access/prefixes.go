package access

import (
	"context"
	"strings"
)

type prefixContextKey struct{}
type accessRefreshContextKey struct{}

// WithAccessRefresh supplies authentication refresh for long-lived client connections.
func WithAccessRefresh(ctx context.Context, refresh func(context.Context) ([]string, *AuthError)) context.Context {
	return context.WithValue(ctx, accessRefreshContextKey{}, refresh)
}

// RefreshAccess checks the current credentials and returns their latest prefix policy.
func RefreshAccess(ctx context.Context) (context.Context, *AuthError) {
	if ctx == nil {
		return ctx, nil
	}
	refresh, _ := ctx.Value(accessRefreshContextKey{}).(func(context.Context) ([]string, *AuthError))
	if refresh == nil {
		return ctx, nil
	}
	prefixes, err := refresh(ctx)
	if err != nil {
		return ctx, err
	}
	return WithAllowedPrefixes(ctx, prefixes), nil
}

// WithAllowedPrefixes attaches an immutable client authorization policy to a request.
func WithAllowedPrefixes(ctx context.Context, prefixes []string) context.Context {
	return context.WithValue(ctx, prefixContextKey{}, append([]string(nil), prefixes...))
}

// AllowedPrefixes returns a copy of the client authorization policy.
func AllowedPrefixes(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	prefixes, _ := ctx.Value(prefixContextKey{}).([]string)
	return append([]string(nil), prefixes...)
}

// AllowsModel requires an explicit authorized prefix for restricted clients.
func AllowsModel(ctx context.Context, model string) bool {
	prefixes := AllowedPrefixes(ctx)
	if len(prefixes) == 0 {
		return true
	}
	for _, prefix := range prefixes {
		prefix = strings.Trim(strings.TrimSpace(prefix), "/")
		if prefix != "" && strings.HasPrefix(model, prefix+"/") && len(model) > len(prefix)+1 {
			return true
		}
	}
	return false
}

// FilterModels preserves model metadata while removing unauthorized model IDs.
func FilterModels(ctx context.Context, models []map[string]any) []map[string]any {
	if len(AllowedPrefixes(ctx)) == 0 {
		return models
	}
	filtered := make([]map[string]any, 0, len(models))
	for _, model := range models {
		id, _ := model["id"].(string)
		if id == "" {
			id, _ = model["name"].(string)
			id = strings.TrimPrefix(id, "models/")
		}
		if AllowsModel(ctx, id) {
			filtered = append(filtered, model)
		}
	}
	return filtered
}
