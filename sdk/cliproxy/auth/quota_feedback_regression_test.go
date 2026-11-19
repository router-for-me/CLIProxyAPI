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
			// Historical version-one records retain explicit 429 attribution
			// without independent deadline provenance. Keep the live checks
			// above and restore this separate supported shape as well.
			if !tc.want && tc.code == "" {
				legacyID := a.ID + "-legacy"
				deadline := time.Now().Add(retry)
				store := NewFileCooldownStateStore(t.TempDir())
				record := CooldownStateRecord{Provider: "codex", AuthID: legacyID, Model: "gpt-6.1-sol", Status: "cooling", NextRetryAfter: deadline, Reason: "quota", Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: deadline}, LastError: &Error{HTTPStatus: 429, Message: tc.message}, UpdatedAt: time.Now()}
				if err := store.Save(context.Background(), []CooldownStateRecord{record}); err != nil {
					t.Fatal(err)
				}
				restored := NewManager(nil, nil, nil)
				if _, err := restored.Register(context.Background(), &Auth{ID: legacyID, Provider: "codex", Status: StatusActive}); err != nil {
					t.Fatal(err)
				}
				restored.SetCooldownStateStore(store)
				if err := restored.RestoreCooldownStates(context.Background()); err != nil {
					t.Fatal(err)
				}
				legacy, _ := restored.GetByID(legacyID)
				state := legacy.ModelStates["gpt-6.1-sol"]
				if state == nil || !state.Quota.Exceeded || state.LastError == nil || state.LastError.HTTPStatus != 429 || state.LastError.Message != tc.message || quotaCodexSubscriptionError(state.LastError) || !state.NextRetryAfter.Equal(deadline) {
					t.Fatal("legacy restore lost explicit non-subscription 429")
				}
				if !legacy.nonQuotaCooldownUntil.IsZero() || !state.nonQuotaCooldownUntil.IsZero() || !legacy.forceCooldownUntil.IsZero() || !state.forceCooldownUntil.IsZero() || !legacy.quotaForceCooldownUntil.IsZero() || !state.quotaForceCooldownUntil.IsZero() || !legacy.credQuotaForceUntil.IsZero() || state.credentialQuotaShadow != nil || state.LastError.Code == ErrorCodeForceCooldown || (legacy.LastError != nil && legacy.LastError.Code == ErrorCodeForceCooldown) {
					t.Fatal("legacy fixture did not isolate explicit error attribution")
				}
				if QuotaRecoveryCandidate(legacy) {
					t.Fatal("legacy explicit Codex 429 accepted as subscription quota")
				}
				reset, _, err := restored.ResetQuotaIfUnchanged(context.Background(), legacy)
				if err != nil {
					t.Fatal(err)
				}
				unchanged, _ := restored.GetByID(legacyID)
				if reset != nil || !reflect.DeepEqual(legacy, unchanged) {
					t.Fatal("plan usage cleared legacy non-subscription Codex cooldown")
				}
			}

		})
	}
}

func TestQuotaRecoveryPreservesIndependentModelShadow(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m := NewManager(nil, nil, nil)
	a, err := m.Register(context.Background(), &Auth{ID: t.Name(), Provider: "codex", Status: StatusActive})
	if err != nil {
		t.Fatal(err)
	}
	model := "gpt-6.1-sol"
	retry := time.Minute
	m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: "codex", Model: model, Error: &Error{HTTPStatus: 404, Message: "model not found"}, RetryAfter: &retry})
	retry = time.Hour
	m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: "codex", Model: model, Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"usage_limit_reached"}}`}, RetryAfter: &retry, CredentialScope: true})
	before, _ := m.GetByID(a.ID)
	state := before.ModelStates[model]
	if state == nil || state.credentialQuotaShadow == nil || state.credentialQuotaShadow.LastError.HTTPStatus != 404 || !state.NextRetryAfter.After(state.credentialQuotaShadow.NextRetryAfter) {
		t.Fatalf("fixture did not retain independent404 under longer quota: %+v", state)
	}
	if QuotaRecoveryCandidate(before) {
		t.Fatal("independent404 admitted as subscription quota")
	}
	got, _, err := m.ResetQuotaIfUnchanged(context.Background(), before)
	after, _ := m.GetByID(a.ID)
	if err != nil || got != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("quota recovery cleared independent model restriction")
	}
}
