package codex

import (
	"sync"
	"testing"
	"time"
)

func TestCooldownManager_SetCooldown(t *testing.T) {
	cm := NewCooldownManager()
	cm.SetCooldown("token1", 1*time.Minute, CooldownReason429)

	if !cm.IsInCooldown("token1") {
		t.Error("expected token1 to be in cooldown")
	}

	if cm.GetCooldownReason("token1") != CooldownReason429 {
		t.Errorf("expected reason %s, got %s", CooldownReason429, cm.GetCooldownReason("token1"))
	}
}

func TestCooldownManager_NotInCooldown(t *testing.T) {
	cm := NewCooldownManager()

	if cm.IsInCooldown("nonexistent") {
		t.Error("expected nonexistent token to not be in cooldown")
	}
}

func TestCooldownManager_ClearCooldown(t *testing.T) {
	cm := NewCooldownManager()
	cm.SetCooldown("token1", 1*time.Minute, CooldownReason429)
	cm.ClearCooldown("token1")

	if cm.IsInCooldown("token1") {
		t.Error("expected token1 to not be in cooldown after clear")
	}
}

func TestCooldownManager_GetRemainingCooldown(t *testing.T) {
	cm := NewCooldownManager()
	cm.SetCooldown("token1", 1*time.Second, CooldownReason429)

	remaining := cm.GetRemainingCooldown("token1")
	if remaining <= 0 || remaining > 1*time.Second {
		t.Errorf("expected remaining cooldown between 0 and 1s, got %v", remaining)
	}
}

func TestCooldownManager_CleanupExpired(t *testing.T) {
	cm := NewCooldownManager()
	cm.SetCooldown("expired1", 1*time.Millisecond, CooldownReason429)
	cm.SetCooldown("expired2", 1*time.Millisecond, CooldownReason429)
	cm.SetCooldown("active", 1*time.Hour, CooldownReason429)

	time.Sleep(10 * time.Millisecond)
	cm.CleanupExpired()

	if cm.IsInCooldown("expired1") {
		t.Error("expected expired1 to be cleaned up")
	}
	if cm.IsInCooldown("expired2") {
		t.Error("expected expired2 to be cleaned up")
	}
	if !cm.IsInCooldown("active") {
		t.Error("expected active to still be in cooldown")
	}
}

func TestCalculateCooldownFor429_WithResetDuration(t *testing.T) {
	tests := []struct {
		name          string
		retryCount    int
		resetDuration time.Duration
		expected      time.Duration
	}{
		{
			name:          "reset duration provided",
			retryCount:    0,
			resetDuration: 10 * time.Minute,
			expected:      10 * time.Minute,
		},
		{
			name:          "reset duration caps at 24h",
			retryCount:    0,
			resetDuration: 48 * time.Hour,
			expected:      LongCooldown,
		},
		{
			name:          "no reset duration, first retry",
			retryCount:    0,
			resetDuration: 0,
			expected:      DefaultShortCooldown,
		},
		{
			name:          "no reset duration, second retry",
			retryCount:    1,
			resetDuration: 0,
			expected:      2 * time.Minute,
		},
		{
			name:          "no reset duration, caps at max",
			retryCount:    10,
			resetDuration: 0,
			expected:      MaxShortCooldown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CalculateCooldownFor429(tt.retryCount, tt.resetDuration)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestCooldownReasonConstants(t *testing.T) {
	if CooldownReason429 != "usage_limit_reached" {
		t.Errorf("unexpected CooldownReason429: %s", CooldownReason429)
	}
	if CooldownReasonSuspended != "account_suspended" {
		t.Errorf("unexpected CooldownReasonSuspended: %s", CooldownReasonSuspended)
	}
}

func TestCooldownManager_Concurrent(t *testing.T) {
	cm := NewCooldownManager()
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func(idx int) {
			defer wg.Done()
			tokenKey := string(rune('a' + idx%26))
			cm.SetCooldown(tokenKey, time.Duration(idx)*time.Millisecond, CooldownReason429)
		}(i)
		go func(idx int) {
			defer wg.Done()
			tokenKey := string(rune('a' + idx%26))
			_ = cm.IsInCooldown(tokenKey)
		}(i)
	}

	wg.Wait()
}

func TestCooldownManager_SetCooldown_OverwritesPrevious(t *testing.T) {
	cm := NewCooldownManager()
	cm.SetCooldown("token1", 1*time.Hour, CooldownReason429)
	cm.SetCooldown("token1", 1*time.Minute, CooldownReasonSuspended)

	remaining := cm.GetRemainingCooldown("token1")
	if remaining > 1*time.Minute {
		t.Errorf("expected cooldown to be overwritten to 1 minute, got %v remaining", remaining)
	}

	if cm.GetCooldownReason("token1") != CooldownReasonSuspended {
		t.Errorf("expected reason to be updated to %s", CooldownReasonSuspended)
	}
}
