package auth

import (
	"context"
	"errors"
	"reflect"
	"testing"
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

