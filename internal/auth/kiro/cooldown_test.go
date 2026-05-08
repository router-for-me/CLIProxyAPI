package kiro

import (
	"sync"
	"testing"
	"time"
)

func TestNewCooldownManager(t *testing.T) {
	cm := NewCooldownManager()
	if cm == nil {
		t.Fatal("expected non-nil CooldownManager")
	}
	if cm.cooldowns == nil {
		t.Error("expected non-nil cooldowns map")
	}
	if cm.reasons == nil {
		t.Error("expected non-nil reasons map")
	}
}

func TestSetCooldown(t *testing.T) {
	cm := NewCooldownManager()
	cm.SetCooldown("token1", 1*time.Minute, CooldownReason429)

	if !cm.IsInCooldown("token1") {
		t.Error("expected token to be in cooldown")
	}
	if cm.GetCooldownReason("token1") != CooldownReason429 {
		t.Errorf("expected reason %s, got %s", CooldownReason429, cm.GetCooldownReason("token1"))
	}
}

func TestIsInCooldown_NotSet(t *testing.T) {
	cm := NewCooldownManager()
	if cm.IsInCooldown("nonexistent") {
		t.Error("expected non-existent token to not be in cooldown")
	}
}

func TestIsInCooldown_Expired(t *testing.T) {
	cm := NewCooldownManager()
	cm.SetCooldown("token1", 1*time.Millisecond, CooldownReason429)

	time.Sleep(10 * time.Millisecond)

	if cm.IsInCooldown("token1") {
		t.Error("expected expired cooldown to return false")
	}
}

func TestGetRemainingCooldown(t *testing.T) {
	cm := NewCooldownManager()
	cm.SetCooldown("token1", 1*time.Second, CooldownReason429)

	remaining := cm.GetRemainingCooldown("token1")
	if remaining <= 0 || remaining > 1*time.Second {
		t.Errorf("expected remaining cooldown between 0 and 1s, got %v", remaining)
	}
}

func TestGetRemainingCooldown_NotSet(t *testing.T) {
	cm := NewCooldownManager()
	remaining := cm.GetRemainingCooldown("nonexistent")
	if remaining != 0 {
		t.Errorf("expected 0 remaining for non-existent, got %v", remaining)
	}
}

func TestGetRemainingCooldown_Expired(t *testing.T) {
	cm := NewCooldownManager()
	cm.SetCooldown("token1", 1*time.Millisecond, CooldownReason429)

	time.Sleep(10 * time.Millisecond)

	remaining := cm.GetRemainingCooldown("token1")
	if remaining != 0 {
		t.Errorf("expected 0 remaining for expired, got %v", remaining)
	}
}

func TestGetCooldownReason(t *testing.T) {
	cm := NewCooldownManager()
	cm.SetCooldown("token1", 1*time.Minute, CooldownReasonSuspended)

	reason := cm.GetCooldownReason("token1")
	if reason != CooldownReasonSuspended {
		t.Errorf("expected reason %s, got %s", CooldownReasonSuspended, reason)
	}
}

func TestGetCooldownReason_NotSet(t *testing.T) {
	cm := NewCooldownManager()
	reason := cm.GetCooldownReason("nonexistent")
	if reason != "" {
		t.Errorf("expected empty reason for non-existent, got %s", reason)
	}
}

func TestClearCooldown(t *testing.T) {
	cm := NewCooldownManager()
	cm.SetCooldown("token1", 1*time.Minute, CooldownReason429)
	cm.ClearCooldown("token1")

	if cm.IsInCooldown("token1") {
		t.Error("expected cooldown to be cleared")
	}
	if cm.GetCooldownReason("token1") != "" {
		t.Error("expected reason to be cleared")
	}
}

func TestClearCooldown_NonExistent(t *testing.T) {
	cm := NewCooldownManager()
	cm.ClearCooldown("nonexistent")
}

func TestCleanupExpired(t *testing.T) {
	cm := NewCooldownManager()
	cm.SetCooldown("expired1", 1*time.Millisecond, CooldownReason429)
	cm.SetCooldown("expired2", 1*time.Millisecond, CooldownReason429)
	cm.SetCooldown("active", 1*time.Hour, CooldownReason429)

	time.Sleep(10 * time.Millisecond)
	cm.CleanupExpired()

	if cm.GetCooldownReason("expired1") != "" {
		t.Error("expected expired1 to be cleaned up")
	}
	if cm.GetCooldownReason("expired2") != "" {
		t.Error("expected expired2 to be cleaned up")
	}
	if cm.GetCooldownReason("active") != CooldownReason429 {
		t.Error("expected active to remain")
	}
}

func TestCalculateCooldownFor429_FirstRetry(t *testing.T) {
	duration := CalculateCooldownFor429(0)
	if duration != DefaultShortCooldown {
		t.Errorf("expected %v for retry 0, got %v", DefaultShortCooldown, duration)
	}
}

func TestCalculateCooldownFor429_Exponential(t *testing.T) {
	d1 := CalculateCooldownFor429(1)
	d2 := CalculateCooldownFor429(2)

	if d2 <= d1 {
		t.Errorf("expected d2 > d1, got d1=%v, d2=%v", d1, d2)
	}
}

func TestCalculateCooldownFor429_MaxCap(t *testing.T) {
	duration := CalculateCooldownFor429(10)
	if duration > MaxShortCooldown {
		t.Errorf("expected max %v, got %v", MaxShortCooldown, duration)
	}
}

func TestCalculateCooldownUntilNextDay(t *testing.T) {
	duration := CalculateCooldownUntilNextDay()
	if duration <= 0 || duration > 24*time.Hour {
		t.Errorf("expected duration between 0 and 24h, got %v", duration)
	}
}

func TestCooldownManager_ConcurrentAccess(t *testing.T) {
	cm := NewCooldownManager()
	const numGoroutines = 50
	const numOperations = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			tokenKey := "token" + string(rune('a'+id%10))
			for j := 0; j < numOperations; j++ {
				switch j % 6 {
				case 0:
					cm.SetCooldown(tokenKey, time.Duration(j)*time.Millisecond, CooldownReason429)
				case 1:
					cm.IsInCooldown(tokenKey)
				case 2:
					cm.GetRemainingCooldown(tokenKey)
				case 3:
					cm.GetCooldownReason(tokenKey)
				case 4:
					cm.ClearCooldown(tokenKey)
				case 5:
					cm.CleanupExpired()
				}
			}
		}(i)
	}

	wg.Wait()
}

func TestCooldownReasonConstants(t *testing.T) {
	if CooldownReason429 != "rate_limit_exceeded" {
		t.Errorf("unexpected CooldownReason429: %s", CooldownReason429)
	}
	if CooldownReasonSuspended != "account_suspended" {
		t.Errorf("unexpected CooldownReasonSuspended: %s", CooldownReasonSuspended)
	}
	if CooldownReasonQuotaExhausted != "quota_exhausted" {
		t.Errorf("unexpected CooldownReasonQuotaExhausted: %s", CooldownReasonQuotaExhausted)
	}
}

func TestDefaultConstants(t *testing.T) {
	if DefaultShortCooldown != 1*time.Minute {
		t.Errorf("unexpected DefaultShortCooldown: %v", DefaultShortCooldown)
	}
	if MaxShortCooldown != 5*time.Minute {
		t.Errorf("unexpected MaxShortCooldown: %v", MaxShortCooldown)
	}
	if LongCooldown != 24*time.Hour {
		t.Errorf("unexpected LongCooldown: %v", LongCooldown)
	}
}

func TestSetCooldown_OverwritesPrevious(t *testing.T) {
	cm := NewCooldownManager()
	cm.SetCooldown("token1", 1*time.Hour, CooldownReason429)
	cm.SetCooldown("token1", 1*time.Minute, CooldownReasonSuspended)

	reason := cm.GetCooldownReason("token1")
	if reason != CooldownReasonSuspended {
		t.Errorf("expected reason to be overwritten to %s, got %s", CooldownReasonSuspended, reason)
	}

	remaining := cm.GetRemainingCooldown("token1")
	if remaining > 1*time.Minute {
		t.Errorf("expected remaining <= 1 minute, got %v", remaining)
	}
}
