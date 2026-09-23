package auth

import (
	"testing"
	"time"
)

func TestIsAuthBlockedForModelExportMatchesPrivateBool(t *testing.T) {
	t.Parallel()

	now := time.Now()
	later := now.Add(2 * time.Hour)
	cases := []struct {
		name  string
		auth  *Auth
		model string
	}{
		{name: "nil", model: "test-model"},
		{
			name:  "healthy",
			auth:  &Auth{ID: "healthy", Status: StatusActive},
			model: "test-model",
		},
		{
			name:  "disabled",
			auth:  &Auth{ID: "disabled", Disabled: true, Status: StatusActive},
			model: "test-model",
		},
		{
			name:  "status disabled",
			auth:  &Auth{ID: "status-disabled", Status: StatusDisabled},
			model: "",
		},
		{
			name:  "quota without recovery",
			auth:  &Auth{ID: "quota", Quota: QuotaState{Exceeded: true}},
			model: "test-model",
		},
		{
			name: "expired recovery",
			auth: &Auth{
				ID:             "expired",
				Unavailable:    true,
				NextRetryAfter: now.Add(-time.Minute),
				Quota: QuotaState{
					Exceeded:      true,
					NextRecoverAt: now.Add(-time.Second),
				},
			},
			model: "",
		},
		{
			name: "model cooldown",
			auth: &Auth{
				ID: "cooldown",
				ModelStates: map[string]*ModelState{
					"test-model(high)": {
						Status:         StatusError,
						Unavailable:    true,
						NextRetryAfter: later,
						Quota:          QuotaState{Exceeded: true, NextRecoverAt: later},
					},
				},
			},
			model: "test-model",
		},
		{
			name: "unavailable without next retry",
			auth: &Auth{
				ID: "no-retry",
				ModelStates: map[string]*ModelState{
					"test-model": {
						Status:      StatusActive,
						Unavailable: true,
						Quota:       QuotaState{Exceeded: true},
					},
				},
			},
			model: "test-model",
		},
		{
			name: "credential quota",
			auth: &Auth{
				ID: "cred-quota",
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "credential_quota",
					NextRecoverAt: later,
				},
			},
			model: "test-model",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			want, _, _ := isAuthBlockedForModel(tc.auth, tc.model, now)
			if got := IsAuthBlockedForModel(tc.auth, tc.model, now); got != want {
				t.Fatalf("IsAuthBlockedForModel() = %v, isAuthBlockedForModel blocked = %v", got, want)
			}
		})
	}
}
