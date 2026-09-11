package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestCooldownWaitBudget(t *testing.T) {
	prevBudget := cooldownWaitBudgetMS.Load()
	prevAttempts := cooldownWaitMaxAttempts.Load()
	prevReclassify := reclassifyQuota403.Load()
	t.Cleanup(func() {
		cooldownWaitBudgetMS.Store(prevBudget)
		cooldownWaitMaxAttempts.Store(prevAttempts)
		reclassifyQuota403.Store(prevReclassify)
	})

	const model = "cooldown-wait-budget-model"

	newManagerWithCoolingAuth := func(t *testing.T) *Manager {
		t.Helper()
		m := NewManager(nil, nil, nil)
		m.SetRetryConfig(5, 90*time.Second, 0)
		next := time.Now().Add(60 * time.Second)
		auth := &Auth{
			ID:       "cooldown-wait-budget-auth",
			Provider: "claude",
			ModelStates: map[string]*ModelState{
				model: {
					Unavailable:    true,
					Status:         StatusError,
					NextRetryAfter: next,
				},
			},
		}
		if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth: %v", errRegister)
		}
		return m
	}

	testCases := []struct {
		name        string
		configure   func()
		attempt     int
		wantWait    time.Duration
		wantRetry   bool
		wantWaitMin time.Duration
		wantWaitMax time.Duration
	}{
		{
			name:      "default budget rejects wait beyond 15s",
			configure: func() { SetCooldownWaitConfig(0, 0, false) },
			attempt:   0,
			wantRetry: false,
		},
		{
			name:        "raised budget allows wait within ceiling",
			configure:   func() { SetCooldownWaitConfig(90000, 3, false) },
			attempt:     0,
			wantRetry:   true,
			wantWaitMin: 55 * time.Second,
			wantWaitMax: 61 * time.Second,
		},
		{
			name:      "max attempts cap stops waiting",
			configure: func() { SetCooldownWaitConfig(90000, 3, false) },
			attempt:   3,
			wantRetry: false,
		},
		{
			name: "explicit zero re-applies defaults after non-zero",
			configure: func() {
				SetCooldownWaitConfig(90000, 3, false)
				SetCooldownWaitConfig(0, 0, false)
			},
			attempt:   0,
			wantRetry: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tc.configure()
			m := newManagerWithCoolingAuth(t)

			_, _, maxWait := m.retrySettings()
			wait, shouldRetry := m.shouldRetryAfterError(&Error{HTTPStatus: http.StatusInternalServerError, Message: "boom"}, tc.attempt, []string{"claude"}, model, maxWait)
			if shouldRetry != tc.wantRetry {
				t.Fatalf("shouldRetryAfterError() = (%v, %t), want retry=%t", wait, shouldRetry, tc.wantRetry)
			}
			if !tc.wantRetry {
				if wait != 0 {
					t.Fatalf("wait = %v, want 0", wait)
				}
				return
			}
			if wait < tc.wantWaitMin || wait > tc.wantWaitMax {
				t.Fatalf("wait = %v, want between %v and %v", wait, tc.wantWaitMin, tc.wantWaitMax)
			}
		})
	}
}

func TestSetCooldownWaitConfig_NormalizesNonPositiveValues(t *testing.T) {
	prevBudget := cooldownWaitBudgetMS.Load()
	prevAttempts := cooldownWaitMaxAttempts.Load()
	prevReclassify := reclassifyQuota403.Load()
	t.Cleanup(func() {
		cooldownWaitBudgetMS.Store(prevBudget)
		cooldownWaitMaxAttempts.Store(prevAttempts)
		reclassifyQuota403.Store(prevReclassify)
	})

	testCases := []struct {
		name         string
		maxWaitMS    int
		maxAttempts  int
		wantBudgetMS int64
		wantAttempts int64
	}{
		{name: "zeros take defaults", maxWaitMS: 0, maxAttempts: 0, wantBudgetMS: 15000, wantAttempts: 3},
		{name: "negatives take defaults", maxWaitMS: -1, maxAttempts: -5, wantBudgetMS: 15000, wantAttempts: 3},
		{name: "explicit values win", maxWaitMS: 45000, maxAttempts: 7, wantBudgetMS: 45000, wantAttempts: 7},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			SetCooldownWaitConfig(tc.maxWaitMS, tc.maxAttempts, false)
			if got := cooldownWaitBudgetMS.Load(); got != tc.wantBudgetMS {
				t.Fatalf("cooldownWaitBudgetMS = %d, want %d", got, tc.wantBudgetMS)
			}
			if got := cooldownWaitMaxAttempts.Load(); got != tc.wantAttempts {
				t.Fatalf("cooldownWaitMaxAttempts = %d, want %d", got, tc.wantAttempts)
			}
		})
	}
}
