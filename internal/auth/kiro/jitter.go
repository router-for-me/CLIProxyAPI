package kiro

import (
	"math/rand"
	"sync"
	"time"
)

// Jitter configuration constants
const (
	// JitterPercent is the default percentage of jitter to apply (±30%)
	JitterPercent = 0.30

	// Human-like delay ranges
	ShortDelayMin  = 50 * time.Millisecond  // Minimum for rapid consecutive operations
	ShortDelayMax  = 200 * time.Millisecond // Maximum for rapid consecutive operations
	NormalDelayMin = 1 * time.Second        // Minimum for normal thinking time
	NormalDelayMax = 3 * time.Second        // Maximum for normal thinking time
	LongDelayMin   = 5 * time.Second        // Minimum for reading/resting
	LongDelayMax   = 10 * time.Second       // Maximum for reading/resting

	// Probability thresholds for human-like behavior
	ShortDelayProbability  = 0.20 // 20% chance of short delay (consecutive ops)
	LongDelayProbability   = 0.05 // 5% chance of long delay (reading/resting)
	NormalDelayProbability = 0.75 // 75% chance of normal delay (thinking)
)

var (
	jitterRand     *rand.Rand
	jitterRandOnce sync.Once
	jitterMu       sync.Mutex
	lastRequestTime time.Time
)

// initJitterRand initializes the random number generator for jitter calculations.
// Uses a time-based seed for unpredictable but reproducible randomness.
func initJitterRand() {
	jitterRandOnce.Do(func() {
		jitterRand = rand.New(rand.NewSource(time.Now().UnixNano()))
	})
}

// RandomDelay generates a random delay between min and max duration.
// Thread-safe implementation using mutex protection.
func RandomDelay(min, max time.Duration) time.Duration {
	initJitterRand()
	jitterMu.Lock()
	defer jitterMu.Unlock()

	if min >= max {
		return min
	}

	rangeMs := max.Milliseconds() - min.Milliseconds()
	randomMs := jitterRand.Int63n(rangeMs)
	return min + time.Duration(randomMs)*time.Millisecond
}

// JitterDelay adds jitter to a base delay.
// Applies ±jitterPercent variation to the base delay.
// For example, JitterDelay(1*time.Second, 0.30) returns a value between 700ms and 1300ms.
func JitterDelay(baseDelay time.Duration, jitterPercent float64) time.Duration {
	initJitterRand()
	jitterMu.Lock()
	defer jitterMu.Unlock()

	if jitterPercent <= 0 || jitterPercent > 1 {
		jitterPercent = JitterPercent
	}

	// Calculate jitter range: base * jitterPercent
	jitterRange := float64(baseDelay) * jitterPercent

	// Generate random value in range [-jitterRange, +jitterRange]
	jitter := (jitterRand.Float64()*2 - 1) * jitterRange

	result := time.Duration(float64(baseDelay) + jitter)
	if result < 0 {
		return 0
	}
	return result
}

// JitterDelayDefault applies the default ±30% jitter to a base delay.
func JitterDelayDefault(baseDelay time.Duration) time.Duration {
	return JitterDelay(baseDelay, JitterPercent)
}

// HumanLikeDelay generates a delay that mimics human behavior patterns.
// The delay is selected based on probability distribution:
//   - 20% chance: Short delay (50-200ms) - simulates consecutive rapid operations
//   - 75% chance: Normal delay (1-3s) - simulates thinking/reading time
//   - 5% chance: Long delay (5-10s) - simulates breaks/reading longer content
//
// Returns the delay duration (caller should call time.Sleep with this value).
func HumanLikeDelay() time.Duration {
	initJitterRand()
	jitterMu.Lock()
	defer jitterMu.Unlock()

	// Track time since last request for adaptive behavior
	now := time.Now()
	timeSinceLastRequest := now.Sub(lastRequestTime)
	lastRequestTime = now

	// If requests are very close together, use short delay
	if timeSinceLastRequest < 500*time.Millisecond && timeSinceLastRequest > 0 {
		rangeMs := ShortDelayMax.Milliseconds() - ShortDelayMin.Milliseconds()
		randomMs := jitterRand.Int63n(rangeMs)
		return ShortDelayMin + time.Duration(randomMs)*time.Millisecond
	}

	// Otherwise, use probability-based selection
	roll := jitterRand.Float64()

	var min, max time.Duration
	switch {
	case roll < ShortDelayProbability:
		// Short delay - consecutive operations
		min, max = ShortDelayMin, ShortDelayMax
	case roll < ShortDelayProbability+LongDelayProbability:
		// Long delay - reading/resting
		min, max = LongDelayMin, LongDelayMax
	default:
		// Normal delay - thinking time
		min, max = NormalDelayMin, NormalDelayMax
	}

	rangeMs := max.Milliseconds() - min.Milliseconds()
	randomMs := jitterRand.Int63n(rangeMs)
	return min + time.Duration(randomMs)*time.Millisecond
}

// ApplyHumanLikeDelay applies human-like delay by sleeping.
// This is a convenience function that combines HumanLikeDelay with time.Sleep.
func ApplyHumanLikeDelay() {
	delay := HumanLikeDelay()
	if delay > 0 {
		time.Sleep(delay)
	}
}

// ExponentialBackoffWithJitter calculates retry delay using exponential backoff with jitter.
// Formula: min(baseDelay * 2^attempt + jitter, maxDelay)
// This helps prevent thundering herd problem when multiple clients retry simultaneously.
func ExponentialBackoffWithJitter(attempt int, baseDelay, maxDelay time.Duration) time.Duration {
	if attempt < 0 {
		attempt = 0
	}

	// Calculate exponential backoff: baseDelay * 2^attempt
	backoff := baseDelay * time.Duration(1<<uint(attempt))
	if backoff > maxDelay {
		backoff = maxDelay
	}

	// Add ±30% jitter
	return JitterDelay(backoff, JitterPercent)
}

// ShouldSkipDelay determines if delay should be skipped based on context.
// Returns true for streaming responses, WebSocket connections, etc.
// This function can be extended to check additional skip conditions.
func ShouldSkipDelay(isStreaming bool) bool {
	return isStreaming
}

// ResetLastRequestTime resets the last request time tracker.
// Useful for testing or when starting a new session.
func ResetLastRequestTime() {
	jitterMu.Lock()
	defer jitterMu.Unlock()
	lastRequestTime = time.Time{}
}
