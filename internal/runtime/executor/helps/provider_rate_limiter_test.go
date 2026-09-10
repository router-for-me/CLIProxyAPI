package helps

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestWaitProviderRateLimit_Disabled(t *testing.T) {
	registry := NewProviderRateLimitRegistry()
	ctx := context.Background()
	if err := registry.Wait(ctx, nil, "p1", "a1"); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	cfg := &config.OpenAICompatibility{
		Name:              "test-provider",
		RequestsPerMinute: 0,
	}
	if err := registry.Wait(ctx, cfg, "test-provider", "a1"); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestWaitProviderRateLimit_EnforcesPacing(t *testing.T) {
	registry := NewProviderRateLimitRegistry()
	cfg := &config.OpenAICompatibility{
		Name:              "limited-provider",
		RequestsPerMinute: 60, // 1 req per second
	}
	ctx := context.Background()

	start := time.Now()
	// First request succeeds immediately
	if err := registry.Wait(ctx, cfg, "limited-provider", "key1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Second request within a burst of 1 must wait ~1s
	if err := registry.Wait(ctx, cfg, "limited-provider", "key1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed.Milliseconds() < 800 {
		t.Fatalf("expected pacing wait of >= 800ms, got %v", elapsed)
	}
}

func TestWaitProviderRateLimit_RechecksRetryAfterForQueuedCall(t *testing.T) {
	registry := NewProviderRateLimitRegistry()
	cfg := &config.OpenAICompatibility{Name: "queued-provider", RequestsPerMinute: 60}
	ctx := context.Background()
	if err := registry.Wait(ctx, cfg, "provider|config:0", "key1"); err != nil {
		t.Fatalf("initial wait: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- registry.Wait(ctx, cfg, "provider|config:0", "key1")
	}()
	time.Sleep(100 * time.Millisecond)
	header := http.Header{}
	header.Set("Retry-After", "2")
	registry.NoteLimited(cfg, "provider|config:0", "key1", header)

	select {
	case err := <-done:
		t.Fatalf("queued call ignored new Retry-After: %v", err)
	case <-time.After(1200 * time.Millisecond):
	}
}

func TestNoteProviderRateLimited_HonoursRetryAfter(t *testing.T) {
	registry := NewProviderRateLimitRegistry()
	cfg := &config.OpenAICompatibility{
		Name:              "rejected-provider",
		RequestsPerMinute: 600, // pacing alone would not delay the next call
	}
	header := http.Header{}
	header.Set("Retry-After", "1")

	registry.NoteLimited(cfg, "rejected-provider", "key1", header)

	ctx := context.Background()
	start := time.Now()
	if err := registry.Wait(ctx, cfg, "rejected-provider", "key1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 800*time.Millisecond {
		t.Fatalf("expected upstream rejection pause of >= 800ms, got %v", elapsed)
	}
}

func TestNoteProviderRateLimited_IgnoredWhenUnconfigured(t *testing.T) {
	registry := NewProviderRateLimitRegistry()
	cfg := &config.OpenAICompatibility{Name: "unlimited-provider"}
	registry.NoteLimited(cfg, "unlimited-provider", "key1", nil)

	ctx := context.Background()
	start := time.Now()
	if err := registry.Wait(ctx, cfg, "unlimited-provider", "key1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("expected no pause without requests-per-minute, waited %v", elapsed)
	}
}

func TestParseRetryAfterPreservesValidZero(t *testing.T) {
	header := http.Header{}
	header.Set("Retry-After", "0")
	if delay, ok := parseRetryAfter(header); !ok || delay != 0 {
		t.Fatalf("parseRetryAfter zero = %v, %v; want 0, true", delay, ok)
	}
	if _, ok := parseRetryAfter(http.Header{}); ok {
		t.Fatal("missing Retry-After reported as valid")
	}
}

func TestNoteProviderRateLimited_CancelledContext(t *testing.T) {
	registry := NewProviderRateLimitRegistry()
	cfg := &config.OpenAICompatibility{
		Name:              "cancelled-provider",
		RequestsPerMinute: 600,
	}
	registry.NoteLimited(cfg, "cancelled-provider", "key1", nil) // no header -> 1 minute fallback

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := registry.Wait(ctx, cfg, "cancelled-provider", "key1"); err == nil {
		t.Fatal("expected context cancellation error while paused")
	}
}
