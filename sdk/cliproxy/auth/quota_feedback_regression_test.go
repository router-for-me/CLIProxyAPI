package auth

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type quotaFeedbackStore struct {
	Store
	fail   bool
	cancel context.CancelFunc
	saved  []*Auth
}

func (s *quotaFeedbackStore) Save(_ context.Context, auth *Auth) (string, error) {
	s.saved = append(s.saved, auth.Clone())
	if len(s.saved) == 1 {
		if s.cancel != nil {
			s.cancel()
		}
		if s.fail {
			return "", errors.New("save failed")
		}
	}
	return "", nil
}

func TestQuotaRecoveryAbortedSaveKeepsNextEditPersistent(t *testing.T) {
	for _, abort := range []string{"failure", "cancel"} {
		for _, edit := range []string{"update", "refresh"} {
			t.Run(abort+"/"+edit, func(t *testing.T) {
				m, a, _, _ := quotaGuardFixture(t)
				m.mu.Lock()
				m.auths[a.ID].Metadata = map[string]any{"type": "codex", "access_token": "old-access", "refresh_token": "old-refresh"}
				m.mu.Unlock()
				a, _ = m.GetByID(a.ID)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				store := &quotaFeedbackStore{fail: abort == "failure"}
				if abort == "cancel" {
					store.cancel = cancel
				}
				m.SetStore(store)
				got, _, err := m.ResetQuotaIfUnchanged(ctx, a)
				if got != nil || err == nil || len(store.saved) != 1 {
					t.Fatalf("abort=%v err=%v saves=%d", got, err, len(store.saved))
				}
				current, _ := m.GetByID(a.ID)
				if !reflect.DeepEqual(current, a) {
					t.Fatal("aborted recovery published state")
				}
				changed := current.Clone()
				changed.Label = "edited"
				changed.Metadata["access_token"] = "new-access"
				changed.Metadata["refresh_token"] = "new-refresh"
				if edit == "refresh" {
					_, err = m.UpdateRefreshedAuth(context.Background(), current, changed)
				} else {
					_, err = m.Update(context.Background(), changed)
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(store.saved) != 2 {
					t.Fatalf("next acknowledged %s was not saved: calls=%d", edit, len(store.saved))
				}
				saved := store.saved[1]
				if saved.Metadata["refresh_token"] != "new-refresh" || saved.Metadata["access_token"] != "new-access" {
					t.Fatalf("rotated credentials lost: %v", saved.Metadata)
				}
				if edit == "update" && saved.Label != "edited" {
					t.Fatal("credential edit not saved")
				}
			})
		}
	}
}

func TestQuotaRecoveryCodexOnlySubscriptionFailures(t *testing.T) {
	for _, tc := range []struct {
		name, message, code string
		credential, want    bool
	}{
		{name: "usage limit", message: `{"error":{"type":"usage_limit_reached","resets_in_seconds":3600}}`, credential: true, want: true},
		{name: "model usage limit", message: `{"error":{"type":"usage_limit_reached","resets_in_seconds":3600}}`, want: true},
		{name: "top-level usage limit", message: `{"type":"usage_limit_reached"}`, credential: true, want: true},
		{name: "classified credential quota", message: "quota exceeded", code: ErrorCodeCredentialQuota, credential: true, want: true},
		{name: "usage name only in message", message: `{"error":{"type":"rate_limit_exceeded","message":"usage_limit_reached"}}`},
		{name: "rate limit", message: `{"error":{"type":"rate_limit_exceeded","message":"too many requests"}}`},
		{name: "model capacity", message: `{"error":{"type":"server_error","code":"model_capacity_exceeded"}}`},
		{name: "transient classification", message: `{"error":{"type":"rate_limit_exceeded"}}`, code: ErrorCodeTransientRateLimit},
		{name: "unknown quota", message: "quota exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withQuotaCooldownEnabled(t)
			m := NewManager(nil, nil, nil)
			a, err := m.Register(context.Background(), &Auth{ID: t.Name(), Provider: "codex", Status: StatusActive})
			if err != nil {
				t.Fatal(err)
			}
			retry := time.Hour
			m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: "codex", Model: "gpt-6.1-sol", Error: &Error{HTTPStatus: 429, Code: tc.code, Message: tc.message}, RetryAfter: &retry, CredentialScope: tc.credential})
			before, _ := m.GetByID(a.ID)
			if got := QuotaRecoveryCandidate(before); got != tc.want {
				t.Fatalf("candidate=%v want=%v", got, tc.want)
			}
			got, _, err := m.ResetQuotaIfUnchanged(context.Background(), before)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want {
				if got == nil || got.Unavailable {
					t.Fatal("subscription recovery stayed blocked")
				}
			} else {
				after, _ := m.GetByID(a.ID)
				if got != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("healthy plan observation cleared unrelated cooldown")
				}
			}
		})
	}
}

