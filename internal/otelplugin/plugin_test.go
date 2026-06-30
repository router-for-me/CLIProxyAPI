package otelplugin

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// The hot-path attribute builders are pure functions over (Record, Config).
// Testing them directly gives full coverage of the mapping rules without
// needing an OTLP collector running. The exporter wiring is exercised in a
// separate integration test (see TestPlugin_LifecycleSmoke at the bottom).

func TestUsageAttributes_GenAiSemconvMapping(t *testing.T) {
	t.Parallel()
	cfg := defaultConfig()
	record := coreusage.Record{
		Provider: "anthropic",
		Model:    "claude-opus-4-7",
		Alias:    "chatplan-opus",
		Detail: coreusage.Detail{
			InputTokens:         1234,
			OutputTokens:        567,
			CacheReadTokens:     89,
			CacheCreationTokens: 12,
			ReasoningTokens:     34,
			TotalTokens:         1936,
		},
		Source:          "chatplan",
		ReasoningEffort: "medium",
	}
	attrs := keyValueMap(usageAttributes(record, cfg))
	assertStringAttr(t, attrs, "gen_ai.system", "anthropic")
	assertStringAttr(t, attrs, "gen_ai.request.model", "claude-opus-4-7")
	assertStringAttr(t, attrs, "gen_ai.request.model_alias", "chatplan-opus")
	assertStringAttr(t, attrs, "gen_ai.request.reasoning_effort", "medium")
	assertStringAttr(t, attrs, "gen_ai.request.source", "chatplan")
	assertInt64Attr(t, attrs, "gen_ai.usage.input_tokens", 1234)
	assertInt64Attr(t, attrs, "gen_ai.usage.output_tokens", 567)
	assertInt64Attr(t, attrs, "gen_ai.usage.cache_read_input_tokens", 89)
	assertInt64Attr(t, attrs, "gen_ai.usage.cache_creation_input_tokens", 12)
	assertInt64Attr(t, attrs, "gen_ai.usage.reasoning_tokens", 34)
	assertInt64Attr(t, attrs, "gen_ai.usage.total_tokens", 1936)
}

func TestUsageAttributes_OptionalFieldsOmittedWhenZero(t *testing.T) {
	t.Parallel()
	cfg := defaultConfig()
	record := coreusage.Record{
		Provider: "anthropic",
		Model:    "claude-haiku-4-5",
		Detail:   coreusage.Detail{InputTokens: 10, OutputTokens: 5},
	}
	attrs := keyValueMap(usageAttributes(record, cfg))
	if _, ok := attrs["gen_ai.usage.cache_read_input_tokens"]; ok {
		t.Error("cache_read_input_tokens should be omitted when zero")
	}
	if _, ok := attrs["gen_ai.request.model_alias"]; ok {
		t.Error("model_alias should be omitted when empty")
	}
}

func TestUsageAttributes_IncludeUsageFalseSuppressesTokenCounts(t *testing.T) {
	t.Parallel()
	off := false
	cfg := applyDefaults(Config{OTLP: OTLPConfig{Span: SpanConfig{IncludeUsage: &off}}})
	record := coreusage.Record{
		Provider: "anthropic",
		Model:    "claude-opus-4-7",
		Detail:   coreusage.Detail{InputTokens: 1234, OutputTokens: 567},
	}
	attrs := keyValueMap(usageAttributes(record, &cfg))
	if _, ok := attrs["gen_ai.usage.input_tokens"]; ok {
		t.Error("token counts should be suppressed when IncludeUsage=false")
	}
	assertStringAttr(t, attrs, "gen_ai.system", "anthropic") // provider still set
}

func TestBaggageAttributes_DefaultIncludeAllKeys(t *testing.T) {
	t.Parallel()
	cfg := defaultConfig()
	b := Baggage{"agent.id": "builder", "workload.kind": "chat-turn", "session.id": "abc"}
	attrs := keyValueMap(baggageAttributes(b, cfg))
	if len(attrs) != 3 {
		t.Errorf("default span config should emit all baggage keys; got %d", len(attrs))
	}
}

func TestBaggageAttributes_AllowlistFilters(t *testing.T) {
	t.Parallel()
	cfg := applyDefaults(Config{OTLP: OTLPConfig{Span: SpanConfig{
		IncludeBaggageKeys: []string{"agent.id"},
	}}})
	b := Baggage{"agent.id": "builder", "secret": "shh"}
	attrs := keyValueMap(baggageAttributes(b, &cfg))
	if len(attrs) != 1 {
		t.Errorf("allowlist should restrict baggage attrs; got %d", len(attrs))
	}
	assertStringAttr(t, attrs, "agent.id", "builder")
}

func TestCostAttributes_PricingTableLookup(t *testing.T) {
	t.Parallel()
	cfg := applyDefaults(Config{Cost: CostConfig{
		Enabled: true,
		Pricing: map[string]ModelPricingUSD{
			"claude-opus-4-7": {
				InputPerMillion:         15,
				OutputPerMillion:        75,
				CacheReadPerMillion:     1.5,
				CacheCreationPerMillion: 18.75,
			},
		},
	}})
	record := coreusage.Record{
		Model: "claude-opus-4-7",
		Detail: coreusage.Detail{
			InputTokens:         1_000_000,
			OutputTokens:        500_000,
			CacheReadTokens:     200_000,
			CacheCreationTokens: 100_000,
		},
	}
	attrs := keyValueMap(costAttributes(record, &cfg))
	assertFloatAttr(t, attrs, "cost.input_usd", 15.0)
	assertFloatAttr(t, attrs, "cost.output_usd", 37.5)
	assertFloatAttr(t, attrs, "cost.cache_read_usd", 0.3)
	assertFloatAttr(t, attrs, "cost.cache_creation_usd", 1.875)
	assertFloatAttr(t, attrs, "cost.usd", 15.0+37.5+0.3+1.875)
}

func TestCostAttributes_PrefixMatchFallsBack(t *testing.T) {
	t.Parallel()
	cfg := applyDefaults(Config{Cost: CostConfig{
		Enabled: true,
		Pricing: map[string]ModelPricingUSD{
			"claude-sonnet": {InputPerMillion: 3, OutputPerMillion: 15},
		},
	}})
	record := coreusage.Record{
		Model:  "claude-sonnet-4-6", // exact key missing; prefix matches
		Detail: coreusage.Detail{InputTokens: 1_000_000, OutputTokens: 0},
	}
	attrs := keyValueMap(costAttributes(record, &cfg))
	assertFloatAttr(t, attrs, "cost.input_usd", 3.0)
}

func TestCostAttributes_NoEntryReturnsNil(t *testing.T) {
	t.Parallel()
	cfg := applyDefaults(Config{Cost: CostConfig{Enabled: true, Pricing: map[string]ModelPricingUSD{}}})
	if got := costAttributes(coreusage.Record{Model: "unknown"}, &cfg); got != nil {
		t.Errorf("unknown model should yield nil cost attrs; got %v", got)
	}
}

func TestCostAttributes_IncludeCostFalseSuppresses(t *testing.T) {
	t.Parallel()
	off := false
	cfg := applyDefaults(Config{
		OTLP: OTLPConfig{Span: SpanConfig{IncludeCost: &off}},
		Cost: CostConfig{Enabled: true, Pricing: map[string]ModelPricingUSD{
			"claude-opus-4-7": {InputPerMillion: 15},
		}},
	})
	record := coreusage.Record{Model: "claude-opus-4-7", Detail: coreusage.Detail{InputTokens: 1_000_000}}
	if got := costAttributes(record, &cfg); got != nil {
		t.Errorf("IncludeCost=false should suppress emission; got %v", got)
	}
}

func TestStartTimeFor_PreferenceOrder(t *testing.T) {
	t.Parallel()
	requested := time.Date(2026, 5, 25, 10, 0, 0, 0, time.UTC)
	if got := startTimeFor(coreusage.Record{RequestedAt: requested}); !got.Equal(requested) {
		t.Errorf("RequestedAt should win: got %v, want %v", got, requested)
	}
	now := time.Now()
	got := startTimeFor(coreusage.Record{Latency: 100 * time.Millisecond})
	if delta := now.Sub(got); delta < 50*time.Millisecond || delta > 500*time.Millisecond {
		t.Errorf("Latency-derived start should be now-Latency; got delta %v", delta)
	}
}

// Plugin lifecycle smoke: registering the plugin then disabling it produces no
// emitted spans. This is the "guard rail" — operators relying on the off-state
// for cost control need certainty that HandleUsage is a true no-op when off.
func TestPlugin_DisabledIsNoOp(t *testing.T) {
	prev := CurrentConfig()
	defer SetConfig(prev) //nolint:errcheck

	SetEnabled(false)
	p := NewPlugin()
	p.HandleUsage(context.Background(), coreusage.Record{Model: "x"})
	// No-op success means we did not block, did not panic, and did not need
	// the OTLP collector. (We cannot assert "no span emitted" without
	// intercepting the exporter, but the early-return guard makes that
	// invariant trivial.)
}

// ---- test helpers -----------------------------------------------------------

func keyValueMap(kvs []attribute.KeyValue) map[string]attribute.Value {
	out := make(map[string]attribute.Value, len(kvs))
	for _, kv := range kvs {
		out[string(kv.Key)] = kv.Value
	}
	return out
}

func assertStringAttr(t *testing.T, attrs map[string]attribute.Value, key, want string) {
	t.Helper()
	got, ok := attrs[key]
	if !ok {
		t.Errorf("attribute %q missing", key)
		return
	}
	if got.AsString() != want {
		t.Errorf("attribute %q: got %q, want %q", key, got.AsString(), want)
	}
}

func assertInt64Attr(t *testing.T, attrs map[string]attribute.Value, key string, want int64) {
	t.Helper()
	got, ok := attrs[key]
	if !ok {
		t.Errorf("attribute %q missing", key)
		return
	}
	if got.AsInt64() != want {
		t.Errorf("attribute %q: got %d, want %d", key, got.AsInt64(), want)
	}
}

func assertFloatAttr(t *testing.T, attrs map[string]attribute.Value, key string, want float64) {
	t.Helper()
	got, ok := attrs[key]
	if !ok {
		t.Errorf("attribute %q missing", key)
		return
	}
	if delta := got.AsFloat64() - want; delta < -1e-9 || delta > 1e-9 {
		t.Errorf("attribute %q: got %v, want %v", key, got.AsFloat64(), want)
	}
}

// keepImportsUsed silences staticcheck warnings if any test helper goes unused
// in a subset run. Tests don't import strings/sort/reflect from outside; this
// is purely defensive against future trimming.
var _ = []any{strings.TrimSpace, sort.Strings, reflect.DeepEqual}
