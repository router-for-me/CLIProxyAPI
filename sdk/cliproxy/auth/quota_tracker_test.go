package auth

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestQuotaTrackerUpdate(t *testing.T) {
	t.Parallel()

	tracker := GetTracker()

	tests := []struct {
		name          string
		initialQuota  QuotaState
		quotaInfo     *QuotaInfo
		wantUsed      int64
		wantLimit     int64
		wantRemaining int64
		wantExceeded  bool
	}{
		{
			name: "update all quota fields",
			initialQuota: QuotaState{
				Used:      0,
				Limit:     0,
				Remaining: 0,
			},
			quotaInfo: &QuotaInfo{
				Used:      50,
				Limit:     100,
				Remaining: 50,
				Exceeded:  false,
			},
			wantUsed:      50,
			wantLimit:     100,
			wantRemaining: 50,
			wantExceeded:  false,
		},
		{
			name: "update with exceeded status",
			initialQuota: QuotaState{
				Used:      80,
				Limit:     100,
				Remaining: 20,
				Exceeded:  false,
			},
			quotaInfo: &QuotaInfo{
				Used:      100,
				Limit:     100,
				Remaining: 0,
				Exceeded:  true,
			},
			wantUsed:      100,
			wantLimit:     100,
			wantRemaining: 0,
			wantExceeded:  true,
		},
		{
			name: "partial update (only used)",
			initialQuota: QuotaState{
				Used:      50,
				Limit:     100,
				Remaining: 50,
			},
			quotaInfo: &QuotaInfo{
				Used:      75,
				Limit:     0, // not updated
				Remaining: 0, // not updated
				Exceeded:  false,
			},
			wantUsed:      75,
			wantLimit:     100, // preserved
			wantRemaining: 50,  // preserved
			wantExceeded:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create auth with initial quota state
			auth := &Auth{
				ID:       "test-auth",
				Provider: "test-provider",
				Quota:    tt.initialQuota,
			}

			// Update quota
			tracker.Update(auth, tt.quotaInfo)

			// Verify quota fields
			if auth.Quota.Used != tt.wantUsed {
				t.Errorf("Update() Quota.Used = %d, want %d", auth.Quota.Used, tt.wantUsed)
			}
			if auth.Quota.Limit != tt.wantLimit {
				t.Errorf("Update() Quota.Limit = %d, want %d", auth.Quota.Limit, tt.wantLimit)
			}
			if auth.Quota.Remaining != tt.wantRemaining {
				t.Errorf("Update() Quota.Remaining = %d, want %d", auth.Quota.Remaining, tt.wantRemaining)
			}
			if auth.Quota.Exceeded != tt.wantExceeded {
				t.Errorf("Update() Quota.Exceeded = %v, want %v", auth.Quota.Exceeded, tt.wantExceeded)
			}

			// Verify UpdatedAt timestamp was set
			if auth.UpdatedAt.IsZero() {
				t.Error("Update() did not set Auth.UpdatedAt")
			}
		})
	}
}

func TestQuotaTrackerUpdate_NilHandling(t *testing.T) {
	t.Parallel()

	tracker := GetTracker()

	// Test nil auth - should not panic
	tracker.Update(nil, &QuotaInfo{Used: 10, Limit: 100, Remaining: 90})

	// Test nil info - should not panic
	auth := &Auth{ID: "test"}
	tracker.Update(auth, nil)

	// Test both nil - should not panic
	tracker.Update(nil, nil)
}

func TestQuotaTrackerUpdate_BackoffIncrement(t *testing.T) {
	t.Parallel()

	tracker := GetTracker()

	auth := &Auth{
		ID:       "test-auth",
		Provider: "test-provider",
		Quota: QuotaState{
			BackoffLevel: 0,
			Exceeded:     false,
		},
	}

	// First exceeded quota
	tracker.Update(auth, &QuotaInfo{
		Exceeded: true,
	})

	if auth.Quota.BackoffLevel != 1 {
		t.Errorf("First exceeded: BackoffLevel = %d, want 1", auth.Quota.BackoffLevel)
	}

	// Second exceeded quota
	tracker.Update(auth, &QuotaInfo{
		Exceeded: true,
	})

	if auth.Quota.BackoffLevel != 2 {
		t.Errorf("Second exceeded: BackoffLevel = %d, want 2", auth.Quota.BackoffLevel)
	}
}

func TestQuotaTrackerUpdate_RecoveryReset(t *testing.T) {
	t.Parallel()

	tracker := GetTracker()

	// Set up auth with exceeded quota and recovery time in the past
	auth := &Auth{
		ID:       "test-auth",
		Provider: "test-provider",
		Quota: QuotaState{
			Exceeded:      true,
			BackoffLevel:  3,
			NextRecoverAt: time.Now().Add(-1 * time.Hour), // past recovery time
			Used:          100,
			Limit:         100,
			Remaining:     0,
		},
	}

	// Update with non-exceeded quota info
	tracker.Update(auth, &QuotaInfo{
		Exceeded:  false,
		Remaining: 50,
	})

	// Should have reset exceeded and backoff
	if auth.Quota.Exceeded {
		t.Error("Recovery: Quota.Exceeded should be false after recovery time")
	}
	if auth.Quota.BackoffLevel != 0 {
		t.Errorf("Recovery: BackoffLevel = %d, want 0", auth.Quota.BackoffLevel)
	}
}

func TestShouldPersist(t *testing.T) {
	t.Parallel()

	tracker := GetTracker()

	tests := []struct {
		name     string
		oldQuota QuotaState
		newQuota QuotaState
		want     bool
	}{
		{
			name: "exceeded status changed (false to true)",
			oldQuota: QuotaState{
				Exceeded:  false,
				Used:      50,
				Limit:     100,
				Remaining: 50,
			},
			newQuota: QuotaState{
				Exceeded:  true,
				Used:      100,
				Limit:     100,
				Remaining: 0,
			},
			want: true,
		},
		{
			name: "exceeded status changed (true to false)",
			oldQuota: QuotaState{
				Exceeded:  true,
				Used:      100,
				Limit:     100,
				Remaining: 0,
			},
			newQuota: QuotaState{
				Exceeded:  false,
				Used:      50,
				Limit:     100,
				Remaining: 50,
			},
			want: true,
		},
		{
			name: "quota percentage dropped by >10% (100% to 85%)",
			oldQuota: QuotaState{
				Exceeded:  false,
				Used:      0,
				Limit:     100,
				Remaining: 100,
			},
			newQuota: QuotaState{
				Exceeded:  false,
				Used:      15,
				Limit:     100,
				Remaining: 85,
			},
			want: true,
		},
		{
			name: "quota percentage dropped by >10% (50% to 30%)",
			oldQuota: QuotaState{
				Exceeded:  false,
				Used:      50,
				Limit:     100,
				Remaining: 50,
			},
			newQuota: QuotaState{
				Exceeded:  false,
				Used:      70,
				Limit:     100,
				Remaining: 30,
			},
			want: true,
		},
		{
			name: "quota percentage dropped by <10% (100% to 95%)",
			oldQuota: QuotaState{
				Exceeded:  false,
				Used:      0,
				Limit:     100,
				Remaining: 100,
			},
			newQuota: QuotaState{
				Exceeded:  false,
				Used:      5,
				Limit:     100,
				Remaining: 95,
			},
			want: false,
		},
		{
			name: "quota percentage dropped by exactly 10% (100% to 90%)",
			oldQuota: QuotaState{
				Exceeded:  false,
				Used:      0,
				Limit:     100,
				Remaining: 100,
			},
			newQuota: QuotaState{
				Exceeded:  false,
				Used:      10,
				Limit:     100,
				Remaining: 90,
			},
			want: false, // >10%, not >=10%
		},
		{
			name: "no change",
			oldQuota: QuotaState{
				Exceeded:  false,
				Used:      50,
				Limit:     100,
				Remaining: 50,
			},
			newQuota: QuotaState{
				Exceeded:  false,
				Used:      50,
				Limit:     100,
				Remaining: 50,
			},
			want: false,
		},
		{
			name: "zero limit (avoid division by zero)",
			oldQuota: QuotaState{
				Exceeded:  false,
				Used:      0,
				Limit:     0,
				Remaining: 0,
			},
			newQuota: QuotaState{
				Exceeded:  false,
				Used:      10,
				Limit:     0,
				Remaining: 0,
			},
			want: false,
		},
		{
			name: "quota increased (90% to 100%)",
			oldQuota: QuotaState{
				Exceeded:  false,
				Used:      10,
				Limit:     100,
				Remaining: 90,
			},
			newQuota: QuotaState{
				Exceeded:  false,
				Used:      0,
				Limit:     100,
				Remaining: 100,
			},
			want: false, // increases don't trigger persistence
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tracker.shouldPersist(tt.oldQuota, tt.newQuota)
			if got != tt.want {
				t.Errorf("shouldPersist() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestQuotaStatePercentage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		quota QuotaState
		want  float64
	}{
		{
			name: "50% remaining",
			quota: QuotaState{
				Used:      50,
				Limit:     100,
				Remaining: 50,
			},
			want: 50.0,
		},
		{
			name: "100% remaining",
			quota: QuotaState{
				Used:      0,
				Limit:     100,
				Remaining: 100,
			},
			want: 100.0,
		},
		{
			name: "0% remaining",
			quota: QuotaState{
				Used:      100,
				Limit:     100,
				Remaining: 0,
			},
			want: 0.0,
		},
		{
			name: "25% remaining",
			quota: QuotaState{
				Used:      75,
				Limit:     100,
				Remaining: 25,
			},
			want: 25.0,
		},
		{
			name: "zero limit (avoid division by zero)",
			quota: QuotaState{
				Used:      0,
				Limit:     0,
				Remaining: 0,
			},
			want: 0.0,
		},
		{
			name: "fractional percentage",
			quota: QuotaState{
				Used:      1,
				Limit:     3,
				Remaining: 2,
			},
			want: 2.0 / 3.0 * 100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.quota.Percentage()
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("Percentage() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestQuotaTrackerPersistCallback(t *testing.T) {
	t.Parallel()

	tracker := &QuotaTracker{} // Use new instance to avoid interfering with other tests

	done := make(chan struct{})
	var callbackAuth *Auth

	// Set up callback
	tracker.SetPersistCallback(func(ctx context.Context, auth *Auth) error {
		callbackAuth = auth
		close(done)
		return nil
	})

	// Create auth
	auth := &Auth{
		ID:       "test-auth",
		Provider: "test-provider",
		Quota: QuotaState{
			Used:      0,
			Limit:     100,
			Remaining: 100,
		},
	}

	// Update with >10% change (should trigger callback)
	tracker.Update(auth, &QuotaInfo{
		Used:      20,
		Limit:     100,
		Remaining: 80,
	})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Persist callback was not invoked for >10% quota change")
	}

	if callbackAuth == nil {
		t.Error("Persist callback received nil auth")
	}
	if callbackAuth != nil && callbackAuth.ID != "test-auth" {
		t.Errorf("Persist callback auth.ID = %s, want test-auth", callbackAuth.ID)
	}
}

func TestQuotaTrackerPersistCallback_NoCallbackSet(t *testing.T) {
	t.Parallel()

	tracker := &QuotaTracker{} // No callback set

	// Create auth
	auth := &Auth{
		ID:       "test-auth",
		Provider: "test-provider",
		Quota: QuotaState{
			Used:      0,
			Limit:     100,
			Remaining: 100,
		},
	}

	// Update with >10% change (should not panic even without callback)
	tracker.Update(auth, &QuotaInfo{
		Used:      20,
		Limit:     100,
		Remaining: 80,
	})

	// No panic = success
}
