package auth

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func quotaRetryFixture(t *testing.T, manager *Manager) *Auth {
	t.Helper()
	id := uuid.NewString()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(id, "claude", []*registry.ModelInfo{{ID: "model-a"}, {ID: "model-b"}})
	t.Cleanup(func() { reg.UnregisterClient(id) })
	auth := &Auth{ID: id, Provider: "claude", Status: StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	return auth
}

func assertQuotaRetryBound(t *testing.T, auth *Auth, interval time.Duration, before, after time.Time) {
	t.Helper()
	for _, model := range []string{"model-a", "model-b"} {
		blocked, _, next := isAuthBlockedForModel(auth, model, before)
		if !blocked || next.Before(before.Add(interval)) || next.After(after.Add(interval)) {
			t.Fatalf("%s blocked=%v retry=%v, want within [%v,%v]", model, blocked, next, before.Add(interval), after.Add(interval))
		}
		if blocked, _, next := isAuthBlockedForModel(auth, model, after.Add(interval+time.Second)); blocked {
			t.Fatalf("%s still blocked after retry interval: %v", model, next)
		}
	}
}

func assertQuotaRetryScheduled(t *testing.T, manager *Manager, auth *Auth, now time.Time) {
	t.Helper()
	manager.scheduler.mu.Lock()
	defer manager.scheduler.mu.Unlock()
	provider := manager.scheduler.providers[auth.Provider]
	if provider == nil {
		t.Fatal("provider missing from scheduler")
	}
	for _, model := range []string{"model-a", "model-b"} {
		shard := provider.ensureModelLocked(model, now)
		picked := shard.pickReadyLocked(false, schedulerStrategyRoundRobin, nil)
		if picked == nil || picked.ID != auth.ID {
			t.Fatalf("scheduler did not promote %s after quota interval", model)
		}
	}
}

func TestQuotaRetryInterval_MarkResult(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, credentialScope := range []bool{false, true} {
			for _, seconds := range []int{0, -1, 1, 300} {
				t.Run(provider+"/"+map[bool]string{false: "model", true: "credential"}[credentialScope]+"/"+strconv.Itoa(seconds), func(t *testing.T) {
					manager := NewManager(nil, nil, nil)
					manager.SetConfig(&internalconfig.Config{QuotaRetryIntervalSeconds: seconds})
					auth := quotaRetryFixture(t, manager)
					auth.Provider = provider
					// Seed old quota-only deadlines to exercise propagation and the
					// final monotonic NextRetryAfter merge, not just fresh 429s.
					old := time.Now().Add(7 * 24 * time.Hour)
					auth.ModelStates = map[string]*ModelState{}
					for _, model := range []string{"model-a", "model-b"} {
						auth.ModelStates[model] = &ModelState{Status: StatusError, Unavailable: true, NextRetryAfter: old, Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: old}}
					}
					if _, err := manager.Update(context.Background(), auth); err != nil {
						t.Fatal(err)
					}
					before := time.Now()
					retry := 7 * 24 * time.Hour
					result := Result{AuthID: auth.ID, Provider: provider, Model: "model-a", CredentialScope: credentialScope, RetryAfter: &retry, Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}}
					manager.MarkResult(context.Background(), result)
					after := time.Now()
					updated, _ := manager.GetByID(auth.ID)
					if seconds <= 0 {
						if blocked, _, _ := isAuthBlockedForModel(updated, "model-a", after.Add(time.Hour)); !blocked {
							t.Fatal("default/non-positive setting shortened provider deadline")
						}
						return
					}
					interval := max(time.Duration(seconds)*time.Second, minQuotaCooldownFloor)
					assertQuotaRetryBound(t, updated, interval, before, after)
					assertQuotaRetryScheduled(t, manager, updated, after.Add(interval+time.Second))
					// A fresh upstream rejection must re-arm cooldown rather than
					// permanently disabling rate limiting after the first retry.
					manager.MarkResult(context.Background(), result)
					updated, _ = manager.GetByID(auth.ID)
					blocked, _, next := isAuthBlockedForModel(updated, "model-a", after)
					if !blocked || next.Before(after.Add(interval)) || next.After(time.Now().Add(interval)) {
						t.Fatal("fresh quota rejection did not re-arm retry deadline")
					}
				})
			}
		}
	}
}

func TestQuotaRetryInterval_IndependentRestrictions(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, test := range []struct {
		name  string
		err   *Error
		retry time.Duration
		quota bool
	}{
		{"longer-retry", nil, 8 * 24 * time.Hour, true},
		{"model-not-found", &Error{HTTPStatus: 404}, 7 * 24 * time.Hour, true},
		{"forced", &Error{HTTPStatus: 429, Code: ErrorCodeForceCooldown}, 7 * 24 * time.Hour, true},
		{"not-quota", nil, 7 * 24 * time.Hour, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			retry := now.Add(test.retry)
			original := retry
			quota := QuotaState{Exceeded: test.quota, Reason: "quota", NextRecoverAt: now.Add(7 * 24 * time.Hour)}
			boundQuotaRetryDeadline(&retry, &quota, test.err, time.Minute, now)
			if retry != original {
				t.Fatalf("independent deadline shortened: %v, want %v", retry, original)
			}
		})
	}
	for _, status := range []Status{StatusDisabled, StatusError} {
		auth := &Auth{Status: status, Quota: QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(7 * 24 * time.Hour)}}
		if status == StatusError {
			auth.LastError = &Error{HTTPStatus: 401}
		}
		if boundAuthQuotaRetries(auth, &internalconfig.Config{QuotaRetryIntervalSeconds: 60}, now) {
			t.Fatal("disabled/unauthorized auth was changed")
		}
	}
	short := now.Add(20 * time.Second)
	quota := QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: short}
	if boundQuotaRetryDeadline(&short, &quota, nil, time.Minute, now) {
		t.Fatal("short provider deadline was changed")
	}
}

func TestQuotaRetryInterval_RestoreAndReload(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(map[bool]string{false: "reload", true: "restore"}[restore], func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			auth := quotaRetryFixture(t, manager)
			next := time.Now().Add(7 * 24 * time.Hour)
			store := &recordingCooldownStateStore{}
			for _, model := range []string{"", "model-a", "model-b"} {
				store.load = append(store.load, CooldownStateRecord{AuthID: auth.ID, Provider: "claude", Model: model, NextRetryAfter: next, Quota: QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: next}})
			}
			manager.SetCooldownStateStore(store)
			if restore {
				manager.SetConfig(&internalconfig.Config{QuotaRetryIntervalSeconds: 60})
			}
			before := time.Now()
			if err := manager.RestoreCooldownStates(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !restore {
				before = time.Now()
				manager.SetConfig(&internalconfig.Config{QuotaRetryIntervalSeconds: 60})
			}
			after := time.Now()
			updated, _ := manager.GetByID(auth.ID)
			assertQuotaRetryBound(t, updated, time.Minute, before, after)
			assertQuotaRetryScheduled(t, manager, updated, after.Add(time.Minute+time.Second))
			if records := store.savedRecords(); len(records) != 3 {
				t.Fatalf("persisted records=%d, want 3", len(records))
			} else {
				for _, record := range records {
					if record.NextRetryAfter != record.Quota.NextRecoverAt || record.NextRetryAfter.After(after.Add(time.Minute)) {
						t.Fatalf("saved stale quota deadline: %+v", record)
					}
				}
			}
		})
	}
}
