package usage

import (
	"context"
	"testing"
	"time"
)

func TestGenerateEnabledDefaultsNilToTrue(t *testing.T) {
	if !GenerateEnabled(nil) {
		t.Fatalf("GenerateEnabled(nil) = false, want true")
	}
}

func TestGenerateEnabledHonorsExplicitFalse(t *testing.T) {
	if GenerateEnabled(GenerateFlag(false)) {
		t.Fatalf("GenerateEnabled(false) = true, want false")
	}
}

func TestGenerateEnabledHonorsExplicitTrue(t *testing.T) {
	if !GenerateEnabled(GenerateFlag(true)) {
		t.Fatalf("GenerateEnabled(true) = false, want true")
	}
}

func TestGenerateFromContextDefaultsMissingToTrue(t *testing.T) {
	if !GenerateFromContext(context.Background()) {
		t.Fatalf("GenerateFromContext(background) = false, want true")
	}
}

func TestGenerateFromContextHonorsExplicitFalse(t *testing.T) {
	ctx := WithGenerate(context.Background(), false)
	if GenerateFromContext(ctx) {
		t.Fatalf("GenerateFromContext(false) = true, want false")
	}
}

func TestRecordOmittedGenerateIsEnabled(t *testing.T) {
	// Existing callers construct Record without setting Generate.
	// Omission must remain distinguishable from explicit false and default to true.
	record := Record{
		Provider: "openai",
		Model:    "gpt-5.4",
	}
	if record.Generate != nil {
		t.Fatalf("Record.Generate = %v, want nil for omitted field", record.Generate)
	}
	if !GenerateEnabled(record.Generate) {
		t.Fatalf("GenerateEnabled(omitted) = false, want true")
	}
}

type captureUsagePlugin struct {
	records chan Record
}

func (p *captureUsagePlugin) HandleUsage(_ context.Context, record Record) {
	p.records <- record
}

func TestManagerNormalizesLegacyCacheInputModeBeforePluginFanout(t *testing.T) {
	manager := NewManager(1)
	t.Cleanup(manager.Stop)
	plugin := &captureUsagePlugin{records: make(chan Record, 1)}
	manager.Register(plugin)

	manager.Publish(context.Background(), Record{
		Provider: "openai",
		Detail: Detail{
			InputTokens:  10,
			CachedTokens: 4,
		},
	})

	select {
	case record := <-plugin.records:
		if record.Detail.CacheInputMode != CacheInputModeIncluded {
			t.Fatalf("cache input mode = %q, want %q", record.Detail.CacheInputMode, CacheInputModeIncluded)
		}
		if !record.Detail.TokenBreakdown.Valid() {
			t.Fatalf("token breakdown = %+v, want valid canonical breakdown", record.Detail.TokenBreakdown)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for usage plugin record")
	}
}
