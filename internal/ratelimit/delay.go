// Package ratelimit provides self-rate-limiting functionality for OAuth request throttling.
package ratelimit

import (
	"context"
	"crypto/rand"
	"math/big"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// RateLimitProvider provides rate limit configuration for providers.
type RateLimitProvider interface {
	GetEffectiveRateLimit(provider string) *config.ProviderRateLimit
}

var (
	providerMu sync.RWMutex
	provider   RateLimitProvider
)

// SetRateLimitProvider sets the global rate limit provider.
// Called during server startup to wire in the management handler.
func SetRateLimitProvider(p RateLimitProvider) {
	providerMu.Lock()
	defer providerMu.Unlock()
	provider = p
}

// GetEffectiveRateLimit returns the effective rate limit for a provider.
// Returns nil if no provider is set or provider not configured.
func GetEffectiveRateLimit(providerName string) *config.ProviderRateLimit {
	providerMu.RLock()
	p := provider
	providerMu.RUnlock()
	if p == nil {
		return nil
	}
	return p.GetEffectiveRateLimit(providerName)
}

// DelayBeforeRequest applies the configured delay before sending a request.
// Returns immediately if provider is not configured or delay is 0.
// Respects context cancellation during the sleep.
func DelayBeforeRequest(ctx context.Context, providerName string) error {
	limit := GetEffectiveRateLimit(providerName)
	if limit == nil {
		return nil
	}
	return applyDelay(ctx, limit.MinDelayMs, limit.MaxDelayMs)
}

func applyDelay(ctx context.Context, minMs, maxMs int) error {
	if minMs <= 0 && maxMs <= 0 {
		return nil
	}

	var delayMs int
	if minMs >= maxMs {
		delayMs = minMs
	} else {
		// Random delay between min and max (inclusive)
		rangeSize := int64(maxMs - minMs + 1)
		n, err := rand.Int(rand.Reader, big.NewInt(rangeSize))
		if err != nil {
			// Fallback to min if random fails
			delayMs = minMs
		} else {
			delayMs = minMs + int(n.Int64())
		}
	}

	if delayMs <= 0 {
		return nil
	}

	return sleepWithContext(ctx, time.Duration(delayMs)*time.Millisecond)
}

func sleepWithContext(ctx context.Context, duration time.Duration) error {
	select {
	case <-time.After(duration):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ChunkDelayer manages delays between streaming chunks.
type ChunkDelayer struct {
	provider string
	isFirst  bool
}

// NewChunkDelayer creates a new ChunkDelayer for the given provider.
func NewChunkDelayer(provider string) *ChunkDelayer {
	return &ChunkDelayer{
		provider: provider,
		isFirst:  true,
	}
}

// DelayIfNeeded applies chunk delay if configured.
// Skips delay on the first call (first chunk should not be delayed).
// Returns ctx.Err() if context is cancelled during delay.
func (c *ChunkDelayer) DelayIfNeeded(ctx context.Context) error {
	if c.isFirst {
		c.isFirst = false
		return nil
	}

	limit := GetEffectiveRateLimit(c.provider)
	if limit == nil || limit.ChunkDelayMs <= 0 {
		return nil
	}

	return sleepWithContext(ctx, time.Duration(limit.ChunkDelayMs)*time.Millisecond)
}
