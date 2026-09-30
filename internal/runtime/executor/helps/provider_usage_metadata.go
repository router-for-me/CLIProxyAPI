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
	if holder, ok := ctx.Value(providerUsageMetadataKey{}).(*providerUsageMetadataHolder); ok && holder != nil {
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

// SetProviderUsageMetadata merges md into the provider's existing metadata
// map. Existing keys in the provider's map are overwritten, but keys NOT
// present in md are preserved — so a flag stamped by the executor (e.g.
// "flex_downgraded": true) survives a later sink write that captures cost
// headers or a mid-stream cost comment. The merge happens against the
// holder's authoritative inner map; readers see the merged result via
// ProviderUsageMetadataFromContext.
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
	inner, ok := holder.md.Metadata[provider].(map[string]any)
	if !ok || inner == nil {
		inner = map[string]any{}
		holder.md.Metadata[provider] = inner
	}
	for k, v := range md {
		inner[k] = v
	}
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
	out := ProviderUsageMetadata{
		EnergyJoules:        holder.md.EnergyJoules,
		ResponseServiceTier: holder.md.ResponseServiceTier,
	}
	if holder.md.Metadata != nil {
		out.Metadata = make(map[string]any, len(holder.md.Metadata))
		for k, v := range holder.md.Metadata {
			if inner, ok := v.(map[string]any); ok {
				cp := make(map[string]any, len(inner))
				for ik, iv := range inner {
					cp[ik] = iv
				}
				out.Metadata[k] = cp
				continue
			}
			out.Metadata[k] = v
		}
	}
	return out
}
