package auth

import (
	"testing"
	"time"
)

func TestHealthTracker_MarkRateLimited(t *testing.T) {
	ht := NewHealthTracker()
	accountID := "test-account"

	ht.MarkRateLimited(accountID, nil)

	h := ht.GetHealth(accountID)
	if h == nil {
		t.Fatal("GetHealth returned nil")
	}
	if h.Status != HealthRateLimited {
		t.Errorf("Status = %v, want %v", h.Status, HealthRateLimited)
	}
	if h.BackoffUntil.IsZero() {
		t.Error("BackoffUntil should be set")
	}
	if !ht.IsAvailable(accountID) {
		// Should not be immediately available
	}
}

func TestHealthTracker_MarkRateLimited_WithRetryAfter(t *testing.T) {
	ht := NewHealthTracker()
	accountID := "test-account"
	retryAfter := 60 * time.Second

	ht.MarkRateLimited(accountID, &retryAfter)

	h := ht.GetHealth(accountID)
	if h == nil {
		t.Fatal("GetHealth returned nil")
	}
	expectedBackoff := time.Now().Add(retryAfter)
	if h.BackoffUntil.Before(expectedBackoff.Add(-time.Second)) || h.BackoffUntil.After(expectedBackoff.Add(time.Second)) {
		t.Errorf("BackoffUntil = %v, expected around %v", h.BackoffUntil, expectedBackoff)
	}
}

func TestHealthTracker_MarkSuccess(t *testing.T) {
	ht := NewHealthTracker()
	accountID := "test-account"

	ht.MarkRateLimited(accountID, nil)
	ht.MarkSuccess(accountID)

	h := ht.GetHealth(accountID)
	if h == nil {
		t.Fatal("GetHealth returned nil")
	}
	if h.Status != HealthAvailable {
		t.Errorf("Status = %v, want %v", h.Status, HealthAvailable)
	}
	if h.BackoffLevel != 0 {
		t.Errorf("BackoffLevel = %d, want 0", h.BackoffLevel)
	}
}

func TestHealthTracker_MarkError_BecomesErroring(t *testing.T) {
	ht := NewHealthTracker()
	accountID := "test-account"

	for i := 0; i < ConsecutiveFailures; i++ {
		ht.MarkError(accountID, &Error{Code: "test", Message: "test error"})
	}

	h := ht.GetHealth(accountID)
	if h == nil {
		t.Fatal("GetHealth returned nil")
	}
	if h.Status != HealthErroring {
		t.Errorf("Status = %v, want %v after %d failures", h.Status, HealthErroring, ConsecutiveFailures)
	}
}

func TestHealthTracker_GetAvailableAccounts(t *testing.T) {
	ht := NewHealthTracker()
	accounts := []string{"a", "b", "c"}

	ht.MarkRateLimited("b", nil)

	available := ht.GetAvailableAccounts(accounts)
	if len(available) != 2 {
		t.Errorf("GetAvailableAccounts() returned %d accounts, want 2", len(available))
	}

	found := make(map[string]bool)
	for _, id := range available {
		found[id] = true
	}
	if !found["a"] || !found["c"] {
		t.Errorf("Expected a and c to be available, got %v", available)
	}
}

func TestHealthTracker_GetLeastRecentlyLimited(t *testing.T) {
	ht := NewHealthTracker()
	accounts := []string{"a", "b", "c"}

	retryA := 60 * time.Second
	retryB := 120 * time.Second
	retryC := 30 * time.Second

	ht.MarkRateLimited("a", &retryA)
	ht.MarkRateLimited("b", &retryB)
	ht.MarkRateLimited("c", &retryC)

	oldest := ht.GetLeastRecentlyLimited(accounts)
	if oldest != "c" {
		t.Errorf("GetLeastRecentlyLimited() = %v, want c (shortest backoff)", oldest)
	}
}

func TestHealthTracker_Reset(t *testing.T) {
	ht := NewHealthTracker()
	accountID := "test-account"

	ht.MarkRateLimited(accountID, nil)
	ht.Reset(accountID)

	h := ht.GetHealth(accountID)
	if h.Status != HealthAvailable {
		t.Errorf("Status = %v, want %v after reset", h.Status, HealthAvailable)
	}
}

func TestAccountHealth_IsAvailable(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name     string
		health   *AccountHealth
		expected bool
	}{
		{"nil health", nil, true},
		{"available status", &AccountHealth{Status: HealthAvailable}, true},
		{"rate limited in backoff", &AccountHealth{Status: HealthRateLimited, BackoffUntil: now.Add(time.Hour)}, false},
		{"rate limited backoff expired", &AccountHealth{Status: HealthRateLimited, BackoffUntil: now.Add(-time.Hour)}, true},
		{"erroring in backoff", &AccountHealth{Status: HealthErroring, BackoffUntil: now.Add(time.Hour)}, false},
		{"erroring backoff expired", &AccountHealth{Status: HealthErroring, BackoffUntil: now.Add(-time.Hour)}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.health.IsAvailable(now); got != tt.expected {
				t.Errorf("IsAvailable() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestCalculateBackoff(t *testing.T) {
	tests := []struct {
		level    int
		expected time.Duration
	}{
		{0, 30 * time.Second},
		{1, 60 * time.Second},
		{2, 120 * time.Second},
		{3, 240 * time.Second},
		{10, MaxBackoff}, // Should cap at max
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			got := calculateBackoff(tt.level)
			if got != tt.expected {
				t.Errorf("calculateBackoff(%d) = %v, want %v", tt.level, got, tt.expected)
			}
		})
	}
}
