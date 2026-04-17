package codex

import (
	"context"
	"math/rand/v2"
	"time"
)

const (
	refreshRetryBaseDelay  = 200 * time.Millisecond
	refreshRetryJitterSpan = 100 * time.Millisecond
)

var (
	refreshRetryJitter = func() time.Duration {
		return randomizedRefreshRetryJitter(refreshRetryJitterSpan)
	}
	refreshRetryWait = waitForRefreshRetry
)

func refreshRetryDelay(attempt int) time.Duration {
	return refreshRetryDelayWithJitter(attempt, refreshRetryJitter())
}

func refreshRetryDelayWithJitter(attempt int, jitter time.Duration) time.Duration {
	if attempt <= 0 {
		return 0
	}
	return exponentialRefreshRetryDelay(attempt) + jitter
}

func exponentialRefreshRetryDelay(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	return refreshRetryBaseDelay * time.Duration(1<<(attempt-1))
}

func randomizedRefreshRetryJitter(span time.Duration) time.Duration {
	if span <= 0 {
		return 0
	}
	maxOffset := int64(span)
	return time.Duration(rand.Int64N(maxOffset*2+1)) - span
}

func waitForRefreshRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
