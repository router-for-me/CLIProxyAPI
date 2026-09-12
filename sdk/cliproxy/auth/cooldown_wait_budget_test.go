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

	newManagerWithCoolingAuth := func(t *testing.T, maxRetryInterval time.Duration, wait time.Duration, cooldownModel string) *Manager {
		t.Helper()
		m := NewManager(nil, nil, nil)
		m.SetRetryConfig(5, maxRetryInterval, 0)
		auth := &Auth{ID: "cooldown-wait-budget-auth", Provider: "claude"}
		if cooldownModel != "" {
			auth.ModelStates = map[string]*ModelState{
				cooldownModel: {
					Unavailable:    true,
					Status:         StatusError,
					NextRetryAfter: time.Now().Add(wait),
				},
			}
		}
		if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth: %v", errRegister)
		}
		return m
	}

	testCases := []struct {
		name        string
		configure   func()
		maxWait     time.Duration
		cooldown    time.Duration
		use429      bool
		attempt     int
		wantRetry   bool
		wantWaitMin time.Duration
		wantWaitMax time.Duration
	}{
		{
			name:      "default budget rejects wait beyond 15s",
			configure: func() { SetCooldownWaitConfig(0, 0, false) },
			cooldown:  60 * time.Second,
			attempt:   0,
			wantRetry: false,
		},
		{
			name:        "raised budget allows wait within ceiling",
			configure:   func() { SetCooldownWaitConfig(90000, 3, false) },
			cooldown:    60 * time.Second,
			attempt:     0,
			wantRetry:   true,
			wantWaitMin: 55 * time.Second,
			wantWaitMax: 61 * time.Second,
		},
		{
			name:      "max attempts cap stops waiting",
			configure: func() { SetCooldownWaitConfig(90000, 3, false) },
			cooldown:  60 * time.Second,
			attempt:   3,
			wantRetry: false,
		},
		{
			name: "explicit zero re-applies defaults after non-zero",
			configure: func() {
				SetCooldownWaitConfig(90000, 3, false)
				SetCooldownWaitConfig(0, 0, false)
			},
			cooldown:  60 * time.Second,
			attempt:   0,
			wantRetry: false,
		},
		{
			// Budget above the max-retry-interval ceiling must never loosen it.
			name:        "budget above max-retry-interval does not loosen ceiling",
			configure:   func() { SetCooldownWaitConfig(90000, 3, false) },
			maxWait:     30 * time.Second,
			cooldown:    25 * time.Second,
			attempt:     0,
			wantRetry:   true,
			wantWaitMin: 20 * time.Second,
			wantWaitMax: 26 * time.Second,
		},
		{
			// The 429 Retry-After path uses the same tightened ceiling: a
			// 20s Retry-After exceeds the default 15s budget, so no retry.
			name:      "budget tightens 429 retry-after ceiling",
			configure: func() { SetCooldownWaitConfig(0, 0, false) },
			use429:    true,
			attempt:   0,
			wantRetry: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tc.configure()
			if tc.maxWait == 0 {
				tc.maxWait = 90 * time.Second
			}
			cooldownModel := model
			if tc.use429 {
				// Fresh model without cooldown state so the 429
				// Retry-After branch is reached instead.
				cooldownModel = ""
			}
			m := newManagerWithCoolingAuth(t, tc.maxWait, tc.cooldown, cooldownModel)

			var errRetry error
			if tc.use429 {
				// No model cooldown on a fresh model name so the 429
				// Retry-After branch is reached.
				errRetry = &retryAfterStatusError{
					status:     http.StatusTooManyRequests,
					message:    "quota exhausted",
					retryAfter: 20 * time.Second,
				}
			} else {
				errRetry = &Error{HTTPStatus: http.StatusInternalServerError, Message: "boom"}
			}
			_, _, maxWait := m.retrySettings()
			wait, shouldRetry := m.shouldRetryAfterError(errRetry, tc.attempt, []string{"claude"}, model, maxWait)
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
