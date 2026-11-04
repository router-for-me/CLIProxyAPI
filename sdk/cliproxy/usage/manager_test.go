package usage

import (
	"context"
	"testing"
	"time"
)

func TestStreamFromContextDefaultsMissingToFalse(t *testing.T) {
	if StreamFromContext(context.Background()) {
		t.Fatalf("StreamFromContext(background) = true, want false")
	}
}

func TestStreamFromContextHonorsExplicitTrue(t *testing.T) {
	ctx := WithStream(context.Background(), true)
	if !StreamFromContext(ctx) {
		t.Fatalf("StreamFromContext(true) = false, want true")
	}
}

func TestRecordStreamField(t *testing.T) {
	record := Record{
		Provider: "openai",
		Model:    "gpt-5.4",
		Stream:   true,
	}
	if !record.Stream {
		t.Fatalf("Record.Stream = false, want true")
	}
}

func TestRecordBaseURLField(t *testing.T) {
	record := Record{
		Provider: "openai",
		Model:    "gpt-5.4",
		BaseURL:  "https://custom-gateway.example.com/v1",
	}
	if record.BaseURL != "https://custom-gateway.example.com/v1" {
		t.Fatalf("Record.BaseURL = %q, want %q", record.BaseURL, "https://custom-gateway.example.com/v1")
	}
}

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

func TestAccessProviderAndIsNativeKeyFromContext(t *testing.T) {
	bg := context.Background()
	if provider := AccessProviderFromContext(bg); provider != "" {
		t.Fatalf("AccessProviderFromContext(bg) = %q, want empty", provider)
	}
	if _, ok := IsNativeKeyFromContext(bg); ok {
		t.Fatalf("IsNativeKeyFromContext(bg) reported ok, want false")
	}

	ctx := WithAccessProvider(bg, "config-inline")
	if provider := AccessProviderFromContext(ctx); provider != "config-inline" {
		t.Fatalf("AccessProviderFromContext(ctx) = %q, want %q", provider, "config-inline")
	}

	ctxNative := WithIsNativeKey(ctx, true)
	if val, ok := IsNativeKeyFromContext(ctxNative); !ok || !val {
		t.Fatalf("IsNativeKeyFromContext(ctxNative) = (%v, %v), want (true, true)", val, ok)
	}

	ctxNonNative := WithIsNativeKey(ctx, false)
	if val, ok := IsNativeKeyFromContext(ctxNonNative); !ok || val {
		t.Fatalf("IsNativeKeyFromContext(ctxNonNative) = (%v, %v), want (false, true)", val, ok)
	}
}

func TestIsNativeAccessProvider(t *testing.T) {
	tests := []struct {
		provider string
		want     bool
	}{
		{"config-inline", true},
		{"CONFIG-INLINE", true},
		{"config-api-key", true},
		{"CONFIG-API-KEY", true},
		{"  config-inline  ", true},
		{"custom-plugin", false},
		{"realtime-client-secret", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsNativeAccessProvider(tt.provider); got != tt.want {
			t.Errorf("IsNativeAccessProvider(%q) = %v, want %v", tt.provider, got, tt.want)
		}
	}
}

func TestRecordNativeKeyFields(t *testing.T) {
	record := Record{
		Provider:       "openai",
		Model:          "gpt-5.4",
		APIKey:         "sk-test-123",
		IsNativeKey:    true,
		AccessProvider: "config-inline",
	}
	if !record.IsNativeKey {
		t.Fatalf("Record.IsNativeKey = false, want true")
	}
	if record.AccessProvider != "config-inline" {
		t.Fatalf("Record.AccessProvider = %q, want %q", record.AccessProvider, "config-inline")
	}
}

type captureUsagePlugin struct {
	records chan Record
}

func (p *captureUsagePlugin) HandleUsage(_ context.Context, record Record) {
	p.records <- record
}

func TestManagerPublishBackfillsContextAuthFields(t *testing.T) {
	mgr := NewManager(10)
	t.Cleanup(mgr.Stop)
	plugin := &captureUsagePlugin{records: make(chan Record, 1)}
	mgr.Register(plugin)

	ctx := WithAPIKey(context.Background(), "sk-ctx-key")
	ctx = WithAccessProvider(ctx, "config-inline")

	mgr.Publish(ctx, Record{
		Provider: "openai",
		Model:    "gpt-5.4",
	})

	select {
	case got := <-plugin.records:
		if got.APIKey != "sk-ctx-key" {
			t.Fatalf("got.APIKey = %q, want sk-ctx-key", got.APIKey)
		}
		if got.AccessProvider != "config-inline" {
			t.Fatalf("got.AccessProvider = %q, want config-inline", got.AccessProvider)
		}
		if !got.IsNativeKey {
			t.Fatalf("got.IsNativeKey = false, want true")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for usage record")
	}
}
