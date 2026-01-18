package config

import (
	"testing"
)

func TestSanitizeSelfRateLimit_Valid(t *testing.T) {
	cfg := &Config{
		SelfRateLimit: map[string]ProviderRateLimit{
			"claude": {MinDelayMs: 100, MaxDelayMs: 500, ChunkDelayMs: 50},
			"codex":  {MinDelayMs: 200, MaxDelayMs: 800, ChunkDelayMs: 30},
		},
	}
	cfg.SanitizeSelfRateLimit()

	if len(cfg.SelfRateLimit) != 2 {
		t.Errorf("expected 2 providers, got %d", len(cfg.SelfRateLimit))
	}
	if cfg.SelfRateLimit["claude"].MinDelayMs != 100 {
		t.Errorf("expected claude MinDelayMs 100, got %d", cfg.SelfRateLimit["claude"].MinDelayMs)
	}
}

func TestSanitizeSelfRateLimit_MinGreaterThanMax(t *testing.T) {
	cfg := &Config{
		SelfRateLimit: map[string]ProviderRateLimit{
			"claude": {MinDelayMs: 500, MaxDelayMs: 100, ChunkDelayMs: 50}, // Invalid: min > max
			"codex":  {MinDelayMs: 100, MaxDelayMs: 500, ChunkDelayMs: 30}, // Valid
		},
	}
	cfg.SanitizeSelfRateLimit()

	if len(cfg.SelfRateLimit) != 1 {
		t.Errorf("expected 1 provider after dropping invalid, got %d", len(cfg.SelfRateLimit))
	}
	if _, ok := cfg.SelfRateLimit["claude"]; ok {
		t.Error("claude should have been dropped due to min > max")
	}
	if _, ok := cfg.SelfRateLimit["codex"]; !ok {
		t.Error("codex should still be present")
	}
}

func TestSanitizeSelfRateLimit_NegativeValues(t *testing.T) {
	cfg := &Config{
		SelfRateLimit: map[string]ProviderRateLimit{
			"claude": {MinDelayMs: -100, MaxDelayMs: 500, ChunkDelayMs: 50},
			"codex":  {MinDelayMs: 100, MaxDelayMs: -500, ChunkDelayMs: 30},
			"vertex": {MinDelayMs: 100, MaxDelayMs: 500, ChunkDelayMs: -30},
			"valid":  {MinDelayMs: 100, MaxDelayMs: 500, ChunkDelayMs: 30},
		},
	}
	cfg.SanitizeSelfRateLimit()

	if len(cfg.SelfRateLimit) != 1 {
		t.Errorf("expected 1 valid provider, got %d", len(cfg.SelfRateLimit))
	}
	if _, ok := cfg.SelfRateLimit["valid"]; !ok {
		t.Error("valid provider should still be present")
	}
}

func TestSanitizeSelfRateLimit_NormalizesProviderName(t *testing.T) {
	cfg := &Config{
		SelfRateLimit: map[string]ProviderRateLimit{
			"Claude":    {MinDelayMs: 100, MaxDelayMs: 500, ChunkDelayMs: 50},
			"  VERTEX ": {MinDelayMs: 100, MaxDelayMs: 500, ChunkDelayMs: 30},
		},
	}
	cfg.SanitizeSelfRateLimit()

	if _, ok := cfg.SelfRateLimit["claude"]; !ok {
		t.Error("Claude should be normalized to 'claude'")
	}
	if _, ok := cfg.SelfRateLimit["vertex"]; !ok {
		t.Error("VERTEX should be normalized to 'vertex'")
	}
	if _, ok := cfg.SelfRateLimit["Claude"]; ok {
		t.Error("original 'Claude' key should not exist after normalization")
	}
}

func TestSanitizeSelfRateLimit_EmptyProvider(t *testing.T) {
	cfg := &Config{
		SelfRateLimit: map[string]ProviderRateLimit{
			"":       {MinDelayMs: 100, MaxDelayMs: 500, ChunkDelayMs: 50},
			"   ":    {MinDelayMs: 100, MaxDelayMs: 500, ChunkDelayMs: 30},
			"claude": {MinDelayMs: 100, MaxDelayMs: 500, ChunkDelayMs: 50},
		},
	}
	cfg.SanitizeSelfRateLimit()

	if len(cfg.SelfRateLimit) != 1 {
		t.Errorf("expected 1 provider after dropping empty, got %d", len(cfg.SelfRateLimit))
	}
}

func TestSanitizeSelfRateLimit_ZeroValues(t *testing.T) {
	// Zero values should be valid (no delay)
	cfg := &Config{
		SelfRateLimit: map[string]ProviderRateLimit{
			"claude": {MinDelayMs: 0, MaxDelayMs: 0, ChunkDelayMs: 0},
		},
	}
	cfg.SanitizeSelfRateLimit()

	if len(cfg.SelfRateLimit) != 1 {
		t.Errorf("expected 1 provider (zero values are valid), got %d", len(cfg.SelfRateLimit))
	}
}

func TestSanitizeSelfRateLimit_NilConfig(t *testing.T) {
	var cfg *Config
	cfg.SanitizeSelfRateLimit() // Should not panic
}

func TestSanitizeSelfRateLimit_EmptyMap(t *testing.T) {
	cfg := &Config{
		SelfRateLimit: map[string]ProviderRateLimit{},
	}
	cfg.SanitizeSelfRateLimit()

	if len(cfg.SelfRateLimit) != 0 {
		t.Errorf("expected empty map, got %d entries", len(cfg.SelfRateLimit))
	}
}
