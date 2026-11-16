package auth

import (
	"context"
	"testing"
	"time"
)

func TestQuotaScheduleAccountReplacement(t *testing.T) {
	for _, tc := range []struct {
		name, provider string
		before, after  map[string]any
		retain         bool
	}{
		{"anonymous Claude token replacement", "claude", map[string]any{"access_token": "token-a"}, map[string]any{"access_token": "token-b"}, false},
		{"anonymous Codex token replacement", "codex", map[string]any{"access_token": "token-a"}, map[string]any{"access_token": "token-b"}, false},
		{"unchanged anonymous credential", "claude", map[string]any{"access_token": "token-a"}, map[string]any{"access_token": "token-a"}, true},
		{"Claude account UUID replacement", "claude", map[string]any{"account_uuid": "account-a", "access_token": "token-a"}, map[string]any{"account_uuid": "account-b", "access_token": "token-b"}, false},
		{"Claude organization replacement", "claude", map[string]any{"account_uuid": "account-a", "organization_uuid": "org-a", "access_token": "token-a"}, map[string]any{"account_uuid": "account-a", "organization_uuid": "org-b", "access_token": "token-b"}, false},
		{"Claude same UUID token refresh", "claude", map[string]any{"account_uuid": "account-a", "organization_uuid": "org-a", "access_token": "token-a"}, map[string]any{"account_uuid": "account-a", "organization_uuid": "org-a", "access_token": "token-b"}, true},
		{"Codex account token refresh", "codex", map[string]any{"account_id": "account-a", "access_token": "token-a"}, map[string]any{"account_id": "account-a", "access_token": "token-b"}, true},
		{"Codex account replacement", "codex", map[string]any{"account_id": "account-a", "access_token": "token-a"}, map[string]any{"account_id": "account-b", "access_token": "token-b"}, false},
		{"legacy email token refresh", "claude", map[string]any{"email": "a@example.test", "access_token": "token-a"}, map[string]any{"email": "a@example.test", "access_token": "token-b"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, &GreedySelector{}, nil)
			auth, err := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "replacement", Provider: tc.provider, Metadata: tc.before})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Round(0)
			schedule := QuotaResetSchedule{WeeklyResetAt: now.Add(time.Hour), ObservedAt: now}
			if manager.RecordQuotaResetScheduleIfUnchanged(context.Background(), auth, schedule) == nil {
				t.Fatal("initial observation rejected")
			}
			updated, err := manager.Update(WithSkipPersist(context.Background()), &Auth{ID: auth.ID, Provider: tc.provider, Metadata: tc.after})
			if err != nil {
				t.Fatal(err)
			}
			if tc.retain {
				if updated.QuotaResetSchedule != schedule {
					t.Fatalf("same account lost schedule: %+v", updated.QuotaResetSchedule)
				}
			} else if updated.QuotaResetSchedule != (QuotaResetSchedule{}) {
				t.Fatalf("replacement inherited previous account reset: %+v", updated.QuotaResetSchedule)
			}
		})
	}
}
