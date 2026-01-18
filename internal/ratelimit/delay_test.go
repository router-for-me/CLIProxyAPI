package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// mockRateLimitProvider implements RateLimitProvider for testing
type mockRateLimitProvider struct {
	limits map[string]*config.ProviderRateLimit
}

func (m *mockRateLimitProvider) GetEffectiveRateLimit(provider string) *config.ProviderRateLimit {
	if m.limits == nil {
		return nil
	}
	return m.limits[provider]
}

func TestDelayBeforeRequest_WithinBounds(t *testing.T) {
	mockProvider := &mockRateLimitProvider{
		limits: map[string]*config.ProviderRateLimit{
			"claude": {MinDelayMs: 50, MaxDelayMs: 100, ChunkDelayMs: 30},
		},
	}
	SetRateLimitProvider(mockProvider)
	defer SetRateLimitProvider(nil)

	ctx := context.Background()
	start := time.Now()
	err := DelayBeforeRequest(ctx, "claude")
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Allow 10ms tolerance for scheduling
	minExpected := 40 * time.Millisecond  // 50 - 10ms tolerance
	maxExpected := 120 * time.Millisecond // 100 + 20ms tolerance
	if elapsed < minExpected || elapsed > maxExpected {
		t.Errorf("delay %v outside expected bounds [%v, %v]", elapsed, minExpected, maxExpected)
	}
}

func TestDelayBeforeRequest_NoDelayForUnconfigured(t *testing.T) {
	mockProvider := &mockRateLimitProvider{
		limits: map[string]*config.ProviderRateLimit{},
	}
	SetRateLimitProvider(mockProvider)
	defer SetRateLimitProvider(nil)

	ctx := context.Background()
	start := time.Now()
	err := DelayBeforeRequest(ctx, "unknown")
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if elapsed > 5*time.Millisecond {
		t.Errorf("expected no delay, got %v", elapsed)
	}
}

func TestDelayBeforeRequest_ContextCancellation(t *testing.T) {
	mockProvider := &mockRateLimitProvider{
		limits: map[string]*config.ProviderRateLimit{
			"claude": {MinDelayMs: 500, MaxDelayMs: 1000, ChunkDelayMs: 30},
		},
	}
	SetRateLimitProvider(mockProvider)
	defer SetRateLimitProvider(nil)

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after a short delay
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := DelayBeforeRequest(ctx, "claude")
	elapsed := time.Since(start)

	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}

	// Should have been cancelled quickly, not waited full delay
	if elapsed > 200*time.Millisecond {
		t.Errorf("expected early cancellation, but took %v", elapsed)
	}
}

func TestDelayBeforeRequest_NoProviderSet(t *testing.T) {
	SetRateLimitProvider(nil)

	ctx := context.Background()
	start := time.Now()
	err := DelayBeforeRequest(ctx, "claude")
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if elapsed > 5*time.Millisecond {
		t.Errorf("expected no delay when provider not set, got %v", elapsed)
	}
}

func TestChunkDelayer_SkipsFirstChunk(t *testing.T) {
	mockProvider := &mockRateLimitProvider{
		limits: map[string]*config.ProviderRateLimit{
			"claude": {MinDelayMs: 50, MaxDelayMs: 100, ChunkDelayMs: 50},
		},
	}
	SetRateLimitProvider(mockProvider)
	defer SetRateLimitProvider(nil)

	ctx := context.Background()
	delayer := NewChunkDelayer("claude")

	// First call should be instant (no delay)
	start := time.Now()
	err := delayer.DelayIfNeeded(ctx)
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if elapsed > 5*time.Millisecond {
		t.Errorf("first chunk should have no delay, got %v", elapsed)
	}

	// Second call should have delay
	start = time.Now()
	err = delayer.DelayIfNeeded(ctx)
	elapsed = time.Since(start)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if elapsed < 40*time.Millisecond || elapsed > 70*time.Millisecond {
		t.Errorf("second chunk delay %v outside expected bounds [40ms, 70ms]", elapsed)
	}
}

func TestChunkDelayer_NoDelayWhenZero(t *testing.T) {
	mockProvider := &mockRateLimitProvider{
		limits: map[string]*config.ProviderRateLimit{
			"claude": {MinDelayMs: 50, MaxDelayMs: 100, ChunkDelayMs: 0},
		},
	}
	SetRateLimitProvider(mockProvider)
	defer SetRateLimitProvider(nil)

	ctx := context.Background()
	delayer := NewChunkDelayer("claude")

	// Skip first call
	_ = delayer.DelayIfNeeded(ctx)

	// Second call should have no delay when ChunkDelayMs is 0
	start := time.Now()
	err := delayer.DelayIfNeeded(ctx)
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if elapsed > 5*time.Millisecond {
		t.Errorf("expected no delay when ChunkDelayMs is 0, got %v", elapsed)
	}
}

func TestChunkDelayer_UnconfiguredProvider(t *testing.T) {
	mockProvider := &mockRateLimitProvider{
		limits: map[string]*config.ProviderRateLimit{},
	}
	SetRateLimitProvider(mockProvider)
	defer SetRateLimitProvider(nil)

	ctx := context.Background()
	delayer := NewChunkDelayer("unknown")

	// Skip first call
	_ = delayer.DelayIfNeeded(ctx)

	// Second call should have no delay for unconfigured provider
	start := time.Now()
	err := delayer.DelayIfNeeded(ctx)
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if elapsed > 5*time.Millisecond {
		t.Errorf("expected no delay for unconfigured provider, got %v", elapsed)
	}
}
