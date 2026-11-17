package auth

import (
	"context"
	"fmt"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"reflect"
	"testing"
	"time"
)

func TestQuotaRecoveryClaudeRetainedMinute(t *testing.T) {
	for _, kind := range []string{"requests", "input tokens", "output tokens"} {
		for _, scope := range []string{"model", "auth"} {
			for _, restored := range []bool{false, true} {
				for _, expired := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/restored=%v/expired=%v", kind, scope, restored, expired), func(t *testing.T) {
						ctx := context.Background()
						prior := quotaCooldownDisabled.Load()
						quotaCooldownDisabled.Store(false)
						t.Cleanup(func() { quotaCooldownDisabled.Store(prior) })
						m := NewManager(nil, nil, nil)
						cfg := &internalconfig.Config{}
						cfg.Claude.ModelLevelCooling = true
						m.SetConfig(cfg)
						store := NewFileCooldownStateStore(t.TempDir())
						m.SetCooldownStateStore(store)
						a, err := m.Register(ctx, &Auth{ID: t.Name(), Provider: "claude", Status: StatusActive})
						if err != nil {
							t.Fatal(err)
						}
						model := ""
						if scope == "model" {
							model = "claude-opus-5-5"
						}
						subscription := func() {
							d := time.Hour
							m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Model: model, Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"rate_limit_error","message":"Number of requests has exceeded your 5-hour rate limit."}}`}, RetryAfter: &d})
						}
						subscription()
						minute := 45 * time.Second
						start := time.Now()
						msg := fmt.Sprintf(`{"error":{"type":"rate_limit_error","message":"This request would exceed the %s per minute rate limit. Please try again later."}}`, kind)
						m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Model: model, Error: &Error{HTTPStatus: 429, Message: msg}, RetryAfter: &minute})
						snapshot, _ := m.GetByID(a.ID)
						deadline := snapshot.nonQuotaCooldownUntil
						if scope == "model" {
							deadline = snapshot.ModelStates[model].nonQuotaCooldownUntil
						}
						if deadline.Before(start.Add(44*time.Second)) || deadline.After(time.Now().Add(46*time.Second)) {
							t.Fatalf("independent minute deadline=%v; want own45s, not composedhour", deadline)
						}
						subscription()
						if restored {
							m = NewManager(nil, nil, nil)
							m.SetConfig(cfg)
							if _, err = m.Register(ctx, &Auth{ID: a.ID, Provider: a.Provider, Status: StatusActive}); err != nil {
								t.Fatal(err)
							}
							m.SetCooldownStateStore(store)
							if err = m.RestoreCooldownStates(ctx); err != nil {
								t.Fatal(err)
							}
						}
						before, _ := m.GetByID(a.ID)
						retained := before.nonQuotaCooldownUntil
						if scope == "model" {
							s := before.ModelStates[model]
							retained = s.nonQuotaCooldownUntil
							if s.credentialQuotaShadow != nil {
								t.Fatal("unexpected shadow")
							}
						}
						if !retained.Equal(deadline) || quotaClaudePerMinuteError(before.LastError) {
							t.Fatal("later subscription or restart lost independent minute provenance")
						}
						if expired {
							m.mu.Lock()
							if scope == "model" {
								m.auths[a.ID].ModelStates[model].nonQuotaCooldownUntil = time.Now().Add(-time.Minute)
							} else {
								m.auths[a.ID].nonQuotaCooldownUntil = time.Now().Add(-time.Minute)
							}
							m.mu.Unlock()
							before, _ = m.GetByID(a.ID)
						}
						savedBefore, err := store.Load(ctx)
						if err != nil {
							t.Fatal(err)
						}
						if got := QuotaRecoveryCandidate(before); got != expired {
							t.Errorf("candidate=%v want%v", got, expired)
						}
						got, _, err := m.ResetQuotaIfUnchanged(ctx, before)
						if err != nil {
							t.Fatal(err)
						}
						after, _ := m.GetByID(a.ID)
						savedAfter, err := store.Load(ctx)
						if err != nil {
							t.Fatal(err)
						}
						if expired {
							if got == nil || after.Unavailable || len(savedAfter) != 0 {
								t.Fatal("expired minute prevented quota recovery")
							}
						} else if got != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(savedBefore, savedAfter) {
							t.Fatal("quota recovery erased active minute restriction")
						}
					})
				}
			}
		}
	}
}

func TestQuotaRecoveryClaudeMinuteProviderOwnership(t *testing.T) {
	prior := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prior) })
	m := NewManager(nil, nil, nil)
	a, err := m.Register(context.Background(), &Auth{ID: t.Name(), Provider: "openai", Status: StatusActive})
	if err != nil {
		t.Fatal(err)
	}
	d := 45 * time.Second
	m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: a.Provider, Model: "model", Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"rate_limit_error","message":"This request would exceed the requests per minute rate limit. Please try again later."}}`}, RetryAfter: &d})
	a, _ = m.GetByID(a.ID)
	if !a.ModelStates["model"].nonQuotaCooldownUntil.IsZero() {
		t.Fatal("Claude body attributed minute throttle to another provider")
	}
}
