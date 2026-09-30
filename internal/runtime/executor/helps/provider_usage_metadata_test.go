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

// TestProviderUsageMetadataMergesAcrossCalls pins the merge contract: a second
// SetProviderUsageMetadata call for the same provider must preserve keys it
// does not carry. The neuralwatt executor relies on this — the retry stamps
// flex_downgraded before the upstream call, and the response sink's later
// cost-header write must not wipe it.
func TestProviderUsageMetadataMergesAcrossCalls(t *testing.T) {
	ctx := EnsureProviderUsageMetadata(context.Background())
	SetProviderUsageMetadata(ctx, "neuralwatt", map[string]any{"flex_downgraded": true})
	SetProviderUsageMetadata(ctx, "neuralwatt", map[string]any{"request_cost_usd": 0.0034})

	md := ProviderUsageMetadataFromContext(ctx)
	inner, ok := md.Metadata["neuralwatt"].(map[string]any)
	if !ok {
		t.Fatalf("neuralwatt metadata missing: %+v", md.Metadata)
	}
	if got, _ := inner["flex_downgraded"].(bool); !got {
		t.Fatalf("flex_downgraded = %v, want true (later write wiped it); metadata = %+v", inner["flex_downgraded"], inner)
	}
	if got := inner["request_cost_usd"]; got != 0.0034 {
		t.Fatalf("request_cost_usd = %v, want 0.0034", got)
	}
}

// TestProviderUsageMetadataMergeOverwritesSameKey pins the other half of the
// merge contract: keys present in BOTH calls take the later value.
func TestProviderUsageMetadataMergeOverwritesSameKey(t *testing.T) {
	ctx := EnsureProviderUsageMetadata(context.Background())
	SetProviderUsageMetadata(ctx, "neuralwatt", map[string]any{"grid_id": "us-west-2"})
	SetProviderUsageMetadata(ctx, "neuralwatt", map[string]any{"grid_id": "eu-central-1"})

	md := ProviderUsageMetadataFromContext(ctx)
	inner := md.Metadata["neuralwatt"].(map[string]any)
	if got := inner["grid_id"]; got != "eu-central-1" {
		t.Fatalf("grid_id = %v, want eu-central-1 (later value must win)", got)
	}
}
