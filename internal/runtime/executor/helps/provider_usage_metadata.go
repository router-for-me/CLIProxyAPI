package helps

import (
	"context"
	"sync"
)

// ProviderUsageMetadata carries provider-specific billing/energy data that a
// provider executor captures from an upstream response and the generic usage
// reporter later folds into the usage record. The holder lives behind a
// pointer in the context so writes performed after the context is derived
// (e.g. a streaming cost comment that arrives mid-stream) remain visible to
// the reporter, which publishes at end-of-stream.
type ProviderUsageMetadata struct {
	EnergyJoules float64
	Metadata     map[string]any
	// ResponseServiceTier is a fallback used only when the response body did
	// not carry a service_tier of its own.
	ResponseServiceTier string
}

type providerUsageMetadataKey struct{}

type providerUsageMetadataHolder struct {
	mu sync.Mutex
	md ProviderUsageMetadata
}

// EnsureProviderUsageMetadata returns ctx carrying a mutable metadata holder.
func EnsureProviderUsageMetadata(ctx context.Context) context.Context {
	if ctx == nil {
		return ctx
	}
	if _, ok := ctx.Value(providerUsageMetadataKey{}).(*providerUsageMetadataHolder); ok {
		return ctx
	}
	return context.WithValue(ctx, providerUsageMetadataKey{}, &providerUsageMetadataHolder{})
}

func providerUsageMetadataHolderFrom(ctx context.Context) *providerUsageMetadataHolder {
	if ctx == nil {
		return nil
	}
	holder, _ := ctx.Value(providerUsageMetadataKey{}).(*providerUsageMetadataHolder)
	return holder
}

// SetProviderUsageMetadata merges md under the provider key. Later calls with
// the same provider overwrite earlier values for the same keys.
func SetProviderUsageMetadata(ctx context.Context, provider string, md map[string]any) {
	holder := providerUsageMetadataHolderFrom(ctx)
	if holder == nil || len(md) == 0 {
		return
	}
	holder.mu.Lock()
	defer holder.mu.Unlock()
	if holder.md.Metadata == nil {
		holder.md.Metadata = map[string]any{}
	}
	holder.md.Metadata[provider] = md
}

// SetProviderEnergyJoules records measured energy for the request.
func SetProviderEnergyJoules(ctx context.Context, joules float64) {
	holder := providerUsageMetadataHolderFrom(ctx)
	if holder == nil {
		return
	}
	holder.mu.Lock()
	defer holder.mu.Unlock()
	holder.md.EnergyJoules = joules
}

// SetProviderResponseServiceTier records the tier the upstream reported
// serving, used as a fallback when the body carried none.
func SetProviderResponseServiceTier(ctx context.Context, tier string) {
	holder := providerUsageMetadataHolderFrom(ctx)
	if holder == nil || tier == "" {
		return
	}
	holder.mu.Lock()
	defer holder.mu.Unlock()
	holder.md.ResponseServiceTier = tier
}

// ProviderUsageMetadataFromContext returns a copy of the captured metadata.
func ProviderUsageMetadataFromContext(ctx context.Context) ProviderUsageMetadata {
	holder := providerUsageMetadataHolderFrom(ctx)
	if holder == nil {
		return ProviderUsageMetadata{}
	}
	holder.mu.Lock()
	defer holder.mu.Unlock()
	out := holder.md
	if holder.md.Metadata != nil {
		out.Metadata = make(map[string]any, len(holder.md.Metadata))
		for k, v := range holder.md.Metadata {
			out.Metadata[k] = v
		}
	}
	return out
}
