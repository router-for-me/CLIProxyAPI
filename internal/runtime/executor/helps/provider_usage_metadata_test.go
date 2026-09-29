package helps

import (
	"context"
	"testing"
)

func TestProviderUsageMetadataRoundTrip(t *testing.T) {
	ctx := EnsureProviderUsageMetadata(context.Background())
	SetProviderUsageMetadata(ctx, "neuralwatt", map[string]any{"grid_id": "us-west-2"})
	SetProviderEnergyJoules(ctx, 42.5)
	SetProviderResponseServiceTier(ctx, "flex")

	md := ProviderUsageMetadataFromContext(ctx)
	if md.EnergyJoules != 42.5 {
		t.Fatalf("EnergyJoules = %v, want 42.5", md.EnergyJoules)
	}
	if md.ResponseServiceTier != "flex" {
		t.Fatalf("ResponseServiceTier = %q, want flex", md.ResponseServiceTier)
	}
	if got := md.Metadata["neuralwatt"].(map[string]any)["grid_id"]; got != "us-west-2" {
		t.Fatalf("grid_id = %v, want us-west-2", got)
	}
}

func TestProviderUsageMetadataAbsentIsZero(t *testing.T) {
	md := ProviderUsageMetadataFromContext(context.Background())
	if md.EnergyJoules != 0 || md.Metadata != nil || md.ResponseServiceTier != "" {
		t.Fatalf("absent holder = %+v, want zero value", md)
	}
}

func TestEnsureProviderUsageMetadataIsIdempotent(t *testing.T) {
	ctx1 := EnsureProviderUsageMetadata(context.Background())
	ctx2 := EnsureProviderUsageMetadata(ctx1)
	if ctx1 != ctx2 {
		t.Fatalf("Ensure must be idempotent; got %v vs %v", ctx1, ctx2)
	}
}

func TestProviderUsageMetadataFromContextCopiesMetadataMap(t *testing.T) {
	ctx := EnsureProviderUsageMetadata(context.Background())
	SetProviderUsageMetadata(ctx, "neuralwatt", map[string]any{"grid_id": "us-west-2"})

	md := ProviderUsageMetadataFromContext(ctx)
	md.Metadata["neuralwatt"].(map[string]any)["grid_id"] = "tampered"

	md2 := ProviderUsageMetadataFromContext(ctx)
	if got := md2.Metadata["neuralwatt"].(map[string]any)["grid_id"]; got != "us-west-2" {
		t.Fatalf("internal state leaked; got %q, want us-west-2", got)
	}
}
