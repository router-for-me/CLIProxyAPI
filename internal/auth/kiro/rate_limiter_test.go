package kiro

import (
	"sync"
	"testing"
	"time"
)

func TestNewRateLimiter(t *testing.T) {
	rl := NewRateLimiter()
	if rl == nil {
		t.Fatal("expected non-nil RateLimiter")
	}
	if rl.states == nil {
		t.Error("expected non-nil states map")
	}
	if rl.minTokenInterval != DefaultMinTokenInterval {
		t.Errorf("expected minTokenInterval %v, got %v", DefaultMinTokenInterval, rl.minTokenInterval)
	}
	if rl.maxTokenInterval != DefaultMaxTokenInterval {
		t.Errorf("expected maxTokenInterval %v, got %v", DefaultMaxTokenInterval, rl.maxTokenInterval)
	}
	if rl.dailyMaxRequests != DefaultDailyMaxRequests {
		t.Errorf("expected dailyMaxRequests %d, got %d", DefaultDailyMaxRequests, rl.dailyMaxRequests)
	}
}

func TestNewRateLimiterWithConfig(t *testing.T) {
	cfg := RateLimiterConfig{
		MinTokenInterval:  5 * time.Second,
		MaxTokenInterval:  15 * time.Second,
		DailyMaxRequests:  100,
		JitterPercent:     0.2,
		BackoffBase:       1 * time.Minute,
		BackoffMax:        30 * time.Minute,
		BackoffMultiplier: 1.5,
		SuspendCooldown:   12 * time.Hour,
	}

	rl := NewRateLimiterWithConfig(cfg)
	if rl.minTokenInterval != 5*time.Second {
		t.Errorf("expected minTokenInterval 5s, got %v", rl.minTokenInterval)
	}
	if rl.maxTokenInterval != 15*time.Second {
		t.Errorf("expected maxTokenInterval 15s, got %v", rl.maxTokenInterval)
	}
	if rl.dailyMaxRequests != 100 {
		t.Errorf("expected dailyMaxRequests 100, got %d", rl.dailyMaxRequests)
	}
}

func TestNewRateLimiterWithConfig_PartialConfig(t *testing.T) {
	cfg := RateLimiterConfig{
		MinTokenInterval: 5 * time.Second,
	}

	rl := NewRateLimiterWithConfig(cfg)
	if rl.minTokenInterval != 5*time.Second {
		t.Errorf("expected minTokenInterval 5s, got %v", rl.minTokenInterval)
	}
	if rl.maxTokenInterval != DefaultMaxTokenInterval {
		t.Errorf("expected default maxTokenInterval, got %v", rl.maxTokenInterval)
	}
}

func TestGetTokenState_NonExistent(t *testing.T) {
	rl := NewRateLimiter()
	state := rl.GetTokenState("nonexistent")
	if state != nil {
		t.Error("expected nil state for non-existent token")
	}
}

func TestIsTokenAvailable_NewToken(t *testing.T) {
	rl := NewRateLimiter()
	if !rl.IsTokenAvailable("newtoken") {
		t.Error("expected new token to be available")
	}
}

func TestMarkTokenFailed(t *testing.T) {
	rl := NewRateLimiter()
	rl.MarkTokenFailed("token1")

	state := rl.GetTokenState("token1")
	if state == nil {
		t.Fatal("expected non-nil state")
	}
	if state.FailCount != 1 {
		t.Errorf("expected FailCount 1, got %d", state.FailCount)
	}
	if state.CooldownEnd.IsZero() {
		t.Error("expected non-zero CooldownEnd")
	}
}

func TestMarkTokenSuccess(t *testing.T) {
	rl := NewRateLimiter()
	rl.MarkTokenFailed("token1")
	rl.MarkTokenFailed("token1")
	rl.MarkTokenSuccess("token1")

	state := rl.GetTokenState("token1")
	if state == nil {
		t.Fatal("expected non-nil state")
	}
	if state.FailCount != 0 {
		t.Errorf("expected FailCount 0, got %d", state.FailCount)
	}
	if !state.CooldownEnd.IsZero() {
		t.Error("expected zero CooldownEnd after success")
	}
}

func TestCheckAndMarkSuspended_Suspended(t *testing.T) {
	rl := NewRateLimiter()

	testCases := []string{
		"Account has been suspended",
		"You are banned from this service",
		"Account disabled",
		"Access denied permanently",
		"Rate limit exceeded",
		"Too many requests",
		"Quota exceeded for today",
	}

	for i, msg := range testCases {
		tokenKey := "token" + string(rune('a'+i))
		if !rl.CheckAndMarkSuspended(tokenKey, msg) {
			t.Errorf("expected suspension detected for: %s", msg)
		}
		state := rl.GetTokenState(tokenKey)
		if !state.IsSuspended {
			t.Errorf("expected IsSuspended true for: %s", msg)
		}
	}
}

func TestCheckAndMarkSuspended_NotSuspended(t *testing.T) {
	rl := NewRateLimiter()

	normalErrors := []string{
		"connection timeout",
		"internal server error",
		"bad request",
		"invalid token format",
	}

	for i, msg := range normalErrors {
		tokenKey := "token" + string(rune('a'+i))
		if rl.CheckAndMarkSuspended(tokenKey, msg) {
			t.Errorf("unexpected suspension for: %s", msg)
		}
	}
}

func TestIsTokenAvailable_Suspended(t *testing.T) {
	rl := NewRateLimiter()
	rl.CheckAndMarkSuspended("token1", "Account suspended")

	if rl.IsTokenAvailable("token1") {
		t.Error("expected suspended token to be unavailable")
	}
}

func TestClearTokenState(t *testing.T) {
	rl := NewRateLimiter()
	rl.MarkTokenFailed("token1")
	rl.ClearTokenState("token1")

	state := rl.GetTokenState("token1")
	if state != nil {
		t.Error("expected nil state after clear")
	}
}

func TestResetSuspension(t *testing.T) {
	rl := NewRateLimiter()
	rl.CheckAndMarkSuspended("token1", "Account suspended")
	rl.ResetSuspension("token1")

	state := rl.GetTokenState("token1")
	if state.IsSuspended {
		t.Error("expected IsSuspended false after reset")
	}
	if state.FailCount != 0 {
		t.Errorf("expected FailCount 0, got %d", state.FailCount)
	}
}

func TestResetSuspension_NonExistent(t *testing.T) {
	rl := NewRateLimiter()
	rl.ResetSuspension("nonexistent")
}

func TestCalculateBackoff_ZeroFailCount(t *testing.T) {
	rl := NewRateLimiter()
	backoff := rl.calculateBackoff(0)
	if backoff != 0 {
		t.Errorf("expected 0 backoff for 0 fails, got %v", backoff)
	}
}

func TestCalculateBackoff_Exponential(t *testing.T) {
	cfg := RateLimiterConfig{
		BackoffBase:       1 * time.Minute,
		BackoffMax:        60 * time.Minute,
		BackoffMultiplier: 2.0,
		JitterPercent:     0.3,
	}
	rl := NewRateLimiterWithConfig(cfg)

	backoff1 := rl.calculateBackoff(1)
	if backoff1 < 40*time.Second || backoff1 > 80*time.Second {
		t.Errorf("expected ~1min (with jitter) for fail 1, got %v", backoff1)
	}

	backoff2 := rl.calculateBackoff(2)
	if backoff2 < 80*time.Second || backoff2 > 160*time.Second {
		t.Errorf("expected ~2min (with jitter) for fail 2, got %v", backoff2)
	}
}

func TestCalculateBackoff_MaxCap(t *testing.T) {
	cfg := RateLimiterConfig{
		BackoffBase:       1 * time.Minute,
		BackoffMax:        10 * time.Minute,
		BackoffMultiplier: 2.0,
		JitterPercent:     0,
	}
	rl := NewRateLimiterWithConfig(cfg)

	backoff := rl.calculateBackoff(10)
	if backoff > 10*time.Minute {
		t.Errorf("expected backoff capped at 10min, got %v", backoff)
	}
}

func TestGetTokenState_ReturnsCopy(t *testing.T) {
	rl := NewRateLimiter()
	rl.MarkTokenFailed("token1")

	state1 := rl.GetTokenState("token1")
	state1.FailCount = 999

	state2 := rl.GetTokenState("token1")
	if state2.FailCount == 999 {
		t.Error("GetTokenState should return a copy")
	}
}

func TestRateLimiter_ConcurrentAccess(t *testing.T) {
	rl := NewRateLimiter()
	const numGoroutines = 50
	const numOperations = 50

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			tokenKey := "token" + string(rune('a'+id%10))
			for j := 0; j < numOperations; j++ {
				switch j % 6 {
				case 0:
					rl.IsTokenAvailable(tokenKey)
				case 1:
					rl.MarkTokenFailed(tokenKey)
				case 2:
					rl.MarkTokenSuccess(tokenKey)
				case 3:
					rl.GetTokenState(tokenKey)
				case 4:
					rl.CheckAndMarkSuspended(tokenKey, "test error")
				case 5:
					rl.ResetSuspension(tokenKey)
				}
			}
		}(i)
	}

	wg.Wait()
}

func TestCalculateInterval_WithinRange(t *testing.T) {
	cfg := RateLimiterConfig{
		MinTokenInterval: 10 * time.Second,
		MaxTokenInterval: 30 * time.Second,
		JitterPercent:    0.3,
	}
	rl := NewRateLimiterWithConfig(cfg)

	minAllowed := 7 * time.Second
	maxAllowed := 40 * time.Second

	for i := 0; i < 100; i++ {
		interval := rl.calculateInterval()
		if interval < minAllowed || interval > maxAllowed {
			t.Errorf("interval %v outside expected range [%v, %v]", interval, minAllowed, maxAllowed)
		}
	}
}
