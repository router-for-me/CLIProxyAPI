package auth

import (
	"testing"
	"time"
)

func TestClassifyHard403(t *testing.T) {
	tests := []struct {
		name     string
		err      *Error
		expected Hard403Type
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: Hard403None,
		},
		{
			name:     "non-403 error",
			err:      &Error{HTTPStatus: 401, Message: "unauthorized"},
			expected: Hard403None,
		},
		{
			name:     "generic 403",
			err:      &Error{HTTPStatus: 403, Message: "access denied"},
			expected: Hard403None,
		},
		{
			name:     "CONSUMER_INVALID in message",
			err:      &Error{HTTPStatus: 403, Message: "CONSUMER_INVALID: project not valid"},
			expected: Hard403ConsumerInvalid,
		},
		{
			name:     "CONSUMER_INVALID in code",
			err:      &Error{HTTPStatus: 403, Code: "CONSUMER_INVALID"},
			expected: Hard403ConsumerInvalid,
		},
		{
			name:     "SERVICE_DISABLED in message",
			err:      &Error{HTTPStatus: 403, Message: "SERVICE_DISABLED: API not enabled"},
			expected: Hard403ServiceDisabled,
		},
		{
			name:     "service not used in project",
			err:      &Error{HTTPStatus: 403, Message: "Gemini API has not been used in project before or it is disabled"},
			expected: Hard403ServiceDisabled,
		},
		{
			name:     "PERMISSION_DENIED in message",
			err:      &Error{HTTPStatus: 403, Message: "Permission denied on resource project abc-123"},
			expected: Hard403PermissionDenied,
		},
		{
			name:     "PERMISSION_DENIED in code",
			err:      &Error{HTTPStatus: 403, Code: "PERMISSION_DENIED"},
			expected: Hard403PermissionDenied,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ClassifyHard403(tt.err)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestIsHard403(t *testing.T) {
	t.Run("returns true for hard 403", func(t *testing.T) {
		err := &Error{HTTPStatus: 403, Message: "CONSUMER_INVALID"}
		if !IsHard403(err) {
			t.Error("expected true for CONSUMER_INVALID")
		}
	})

	t.Run("returns false for soft 403", func(t *testing.T) {
		err := &Error{HTTPStatus: 403, Message: "access denied"}
		if IsHard403(err) {
			t.Error("expected false for generic 403")
		}
	})
}

func TestOpenCircuitBreaker(t *testing.T) {
	SetCircuitBreakerConfig(true, 600, 1800, 0)

	t.Run("opens circuit breaker", func(t *testing.T) {
		auth := &Auth{ID: "test-auth", Provider: "gemini"}
		now := time.Now()

		OpenCircuitBreaker(auth, Hard403ConsumerInvalid, now)

		if !auth.CircuitBreaker.Open {
			t.Error("circuit breaker should be open")
		}
		if auth.CircuitBreaker.Reason != Hard403ConsumerInvalid {
			t.Errorf("expected CONSUMER_INVALID, got %v", auth.CircuitBreaker.Reason)
		}
		if auth.CircuitBreaker.FailureCount != 1 {
			t.Errorf("expected failure count 1, got %d", auth.CircuitBreaker.FailureCount)
		}
		expectedCooldown := now.Add(600 * time.Second)
		if !auth.CircuitBreaker.CooldownUntil.Equal(expectedCooldown) {
			t.Errorf("expected cooldown until %v, got %v", expectedCooldown, auth.CircuitBreaker.CooldownUntil)
		}
	})

	t.Run("increments failure count", func(t *testing.T) {
		auth := &Auth{ID: "test-auth-2", Provider: "gemini"}
		now := time.Now()

		OpenCircuitBreaker(auth, Hard403ConsumerInvalid, now)
		OpenCircuitBreaker(auth, Hard403ConsumerInvalid, now.Add(time.Minute))

		if auth.CircuitBreaker.FailureCount != 2 {
			t.Errorf("expected failure count 2, got %d", auth.CircuitBreaker.FailureCount)
		}
	})

	t.Run("does nothing for Hard403None", func(t *testing.T) {
		auth := &Auth{ID: "test-auth-3", Provider: "gemini"}
		now := time.Now()

		OpenCircuitBreaker(auth, Hard403None, now)

		if auth.CircuitBreaker.Open {
			t.Error("circuit breaker should not open for Hard403None")
		}
	})

	t.Run("does nothing when disabled", func(t *testing.T) {
		SetCircuitBreakerConfig(false, 600, 1800, 0)
		defer SetCircuitBreakerConfig(true, 600, 1800, 0)

		auth := &Auth{ID: "test-auth-4", Provider: "gemini"}
		now := time.Now()

		OpenCircuitBreaker(auth, Hard403ConsumerInvalid, now)

		if auth.CircuitBreaker.Open {
			t.Error("circuit breaker should not open when disabled")
		}
	})
}

func TestCloseCircuitBreaker(t *testing.T) {
	t.Run("closes circuit breaker", func(t *testing.T) {
		auth := &Auth{
			ID:       "test-auth",
			Provider: "gemini",
			CircuitBreaker: CircuitBreakerState{
				Open:          true,
				Reason:        Hard403ConsumerInvalid,
				CooldownUntil: time.Now().Add(time.Hour),
				FailureCount:  3,
			},
		}

		CloseCircuitBreaker(auth)

		if auth.CircuitBreaker.Open {
			t.Error("circuit breaker should be closed")
		}
		if auth.CircuitBreaker.Reason != Hard403None {
			t.Errorf("expected Hard403None, got %v", auth.CircuitBreaker.Reason)
		}
		if auth.CircuitBreaker.FailureCount != 0 {
			t.Errorf("expected failure count 0, got %d", auth.CircuitBreaker.FailureCount)
		}
	})
}

func TestIsCircuitBreakerOpen(t *testing.T) {
	SetCircuitBreakerConfig(true, 600, 1800, 0)

	t.Run("returns true when open and not expired", func(t *testing.T) {
		auth := &Auth{
			ID:       "test-auth",
			Provider: "gemini",
			CircuitBreaker: CircuitBreakerState{
				Open:          true,
				CooldownUntil: time.Now().Add(time.Hour),
			},
		}

		if !IsCircuitBreakerOpen(auth, time.Now()) {
			t.Error("expected circuit breaker to be open")
		}
	})

	t.Run("returns false when expired", func(t *testing.T) {
		auth := &Auth{
			ID:       "test-auth-2",
			Provider: "gemini",
			CircuitBreaker: CircuitBreakerState{
				Open:          true,
				CooldownUntil: time.Now().Add(-time.Minute),
			},
		}

		if IsCircuitBreakerOpen(auth, time.Now()) {
			t.Error("expected circuit breaker to report as not open after expiry")
		}
		// IsCircuitBreakerOpen no longer auto-closes; it just returns false for expired breakers
	})

	t.Run("CheckAndCloseExpiredCircuitBreaker closes expired breaker", func(t *testing.T) {
		auth := &Auth{
			ID:       "test-auth-close",
			Provider: "gemini",
			CircuitBreaker: CircuitBreakerState{
				Open:          true,
				Reason:        Hard403ConsumerInvalid,
				CooldownUntil: time.Now().Add(-time.Minute),
				FailureCount:  1,
			},
		}

		if CheckAndCloseExpiredCircuitBreaker(auth, time.Now()) {
			t.Error("expected CheckAndCloseExpiredCircuitBreaker to return false for expired breaker")
		}
		if auth.CircuitBreaker.Open {
			t.Error("circuit breaker should have been closed")
		}
	})

	t.Run("returns false when not open", func(t *testing.T) {
		auth := &Auth{
			ID:       "test-auth-3",
			Provider: "gemini",
			CircuitBreaker: CircuitBreakerState{
				Open: false,
			},
		}

		if IsCircuitBreakerOpen(auth, time.Now()) {
			t.Error("expected circuit breaker to be closed")
		}
	})

	t.Run("returns false when disabled", func(t *testing.T) {
		SetCircuitBreakerConfig(false, 600, 1800, 0)
		defer SetCircuitBreakerConfig(true, 600, 1800, 0)

		auth := &Auth{
			ID:       "test-auth-4",
			Provider: "gemini",
			CircuitBreaker: CircuitBreakerState{
				Open:          true,
				CooldownUntil: time.Now().Add(time.Hour),
			},
		}

		if IsCircuitBreakerOpen(auth, time.Now()) {
			t.Error("expected circuit breaker check to return false when disabled")
		}
	})
}

func TestShouldRetryHard403(t *testing.T) {
	t.Run("returns false when hard403Retry is 0", func(t *testing.T) {
		SetCircuitBreakerConfig(true, 600, 1800, 0)
		if ShouldRetryHard403() {
			t.Error("expected false when hard403Retry is 0")
		}
	})

	t.Run("returns true when hard403Retry > 0", func(t *testing.T) {
		SetCircuitBreakerConfig(true, 600, 1800, 1)
		defer SetCircuitBreakerConfig(true, 600, 1800, 0)

		if !ShouldRetryHard403() {
			t.Error("expected true when hard403Retry > 0")
		}
	})
}

func TestGetHard403MaxRetries(t *testing.T) {
	SetCircuitBreakerConfig(true, 600, 1800, 3)
	defer SetCircuitBreakerConfig(true, 600, 1800, 0)

	if GetHard403MaxRetries() != 3 {
		t.Errorf("expected 3, got %d", GetHard403MaxRetries())
	}
}

func TestGetCooldownDurations(t *testing.T) {
	SetCircuitBreakerConfig(true, 300, 900, 0)
	defer SetCircuitBreakerConfig(true, 600, 1800, 0)

	if GetHard403Cooldown() != 300*time.Second {
		t.Errorf("expected 300s, got %v", GetHard403Cooldown())
	}
	if GetSoft403Cooldown() != 900*time.Second {
		t.Errorf("expected 900s, got %v", GetSoft403Cooldown())
	}
}
