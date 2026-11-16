package auth

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
)

func quotaProviderForceManager(t *testing.T) *Manager {
	t.Helper()
	prior := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prior) })
	return NewManager(nil, nil, nil)
}

func TestQuotaRecoveryClaudeMinuteRestriction(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		want          bool
	}{
		{"requests", "This request would exceed the requests per minute rate limit. Please try again later.", false},
		{"input tokens", "This request would exceed the input tokens per minute rate limit. Please try again later.", false},
		{"output tokens", "This request would exceed the output tokens per minute rate limit. Please try again later.", false},
		{"normalized message", "  THIS REQUEST WOULD EXCEED THE REQUESTS PER MINUTE RATE LIMIT. PLEASE TRY AGAIN LATER.  ", false},
		{"five hour subscription", "Number of requests has exceeded your 5-hour rate limit.", true},
		{"weekly subscription", "This request would exceed your account's rate limit. Please try again later.", true},
		{"negated minute mention", "This is not a requests per minute error; your 7-day usage limit is exhausted.", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := quotaProviderForceManager(t)
			a, err := m.Register(context.Background(), &Auth{ID: t.Name(), Provider: "claude", Status: StatusActive})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(map[string]any{"error": map[string]string{"type": "rate_limit_error", "message": tc.message}})
			if err != nil {
				t.Fatal(err)
			}
			retry := 45 * time.Second
			m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: "claude", Model: "claude-opus-5-5", Error: &Error{HTTPStatus: 429, Message: string(raw)}, RetryAfter: &retry})
			before, _ := m.GetByID(a.ID)
			state := before.ModelStates["claude-opus-5-5"]
			if state == nil || !state.Quota.Exceeded || state.NextRetryAfter.After(time.Now().Add(time.Minute)) {
				t.Fatal("fixture did not retain short upstream rate-limit cooldown")
			}
			if got := QuotaRecoveryCandidate(before); got != tc.want {
				t.Fatalf("candidate=%v want=%v", got, tc.want)
			}
			got, _, err := m.ResetQuotaIfUnchanged(context.Background(), before)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want {
				if got == nil || got.Unavailable {
					t.Fatal("Claude subscription reset stayed blocked")
				}
			} else {
				after, _ := m.GetByID(a.ID)
				if got != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("healthy plan usage cleared an independent per-minute throttle")
				}
			}
		})
	}
}

func TestQuotaRecoveryRetainedForceDeadline(t *testing.T) {
	for _, kind := range []string{"auth", "credential", "model"} {
		for _, expired := range []bool{false, true} {
			suffix := "active"
			if expired {
				suffix = "expired"
			}
			t.Run(kind+"/"+suffix, func(t *testing.T) {
				m := quotaProviderForceManager(t)
				store := NewFileCooldownStateStore(t.TempDir())
				m.SetCooldownStateStore(store)
				a, err := m.Register(context.Background(), &Auth{ID: t.Name(), Provider: "codex", Status: StatusActive})
				if err != nil {
					t.Fatal(err)
				}
				model := ""
				if kind != "auth" {
					model = "gpt-6.1-sol"
				}
				forced := 45 * time.Second
				status := 429
				if kind == "auth" {
					status = 503
				}
				m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: "codex", Model: model, Error: &Error{HTTPStatus: status, Code: ErrorCodeForceCooldown, Message: "operator forced cooldown"}, RetryAfter: &forced, CredentialScope: kind == "credential"})
				quota := time.Hour
				m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: "codex", Model: model, Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"usage_limit_reached"}}`}, RetryAfter: &quota, CredentialScope: kind != "model"})
				if kind == "credential" {
					restored := quotaProviderForceManager(t)
					if _, err = restored.Register(context.Background(), &Auth{ID: a.ID, Provider: "codex", Status: StatusActive}); err != nil {
						t.Fatal(err)
					}
					restored.SetCooldownStateStore(store)
					if err = restored.RestoreCooldownStates(context.Background()); err != nil {
						t.Fatal(err)
					}
					m = restored
				}
				before, _ := m.GetByID(a.ID)
				if before.LastError == nil || before.LastError.Code == ErrorCodeForceCooldown || !strings.Contains(before.LastError.Message, "usage_limit_reached") {
					t.Fatal("later subscription error did not replace the displayed force error")
				}
				now := time.Now()
				switch kind {
				case "auth":
					if len(before.ModelStates) != 0 || !effectiveAuthForceUntil(before).After(now) || !effectiveCredQuotaForceUntil(before).IsZero() {
						t.Fatal("fixture did not isolate the retained auth force deadline")
					}
				case "credential":
					if !effectiveAuthForceUntil(before).IsZero() || !effectiveCredQuotaForceUntil(before).After(now) || !effectiveModelForceUntil(before.ModelStates[model]).IsZero() {
						t.Fatal("restart did not isolate the retained credential force deadline")
					}
				case "model":
					if !effectiveAuthForceUntil(before).IsZero() || !effectiveCredQuotaForceUntil(before).IsZero() || !effectiveModelForceUntil(before.ModelStates[model]).After(now) || before.ModelStates[model].credentialQuotaShadow != nil {
						t.Fatal("fixture did not isolate the retained model force deadline")
					}
				}
				if expired {
					// Represent the retained deadline after natural expiry without sleeping.
					m.mu.Lock()
					current := m.auths[a.ID]
					switch kind {
					case "auth":
						current.forceCooldownUntil = now.Add(-time.Minute)
						current.nonQuotaCooldownUntil = now.Add(-time.Minute)
					case "credential":
						current.credQuotaForceUntil = now.Add(-time.Minute)
					case "model":
						current.ModelStates[model].forceCooldownUntil = now.Add(-time.Minute)
					}
					m.mu.Unlock()
					before, _ = m.GetByID(a.ID)
				}
				if got := QuotaRecoveryCandidate(before); got != expired {
					t.Fatalf("candidate=%v want=%v; later quota must not erase an active operator force", got, expired)
				}
				got, _, err := m.ResetQuotaIfUnchanged(context.Background(), before)
				if err != nil {
					t.Fatal(err)
				}
				if expired {
					if got == nil || got.Unavailable {
						t.Fatal("expired force prevented confirmed quota recovery")
					}
				} else {
					after, _ := m.GetByID(a.ID)
					if got != nil || !reflect.DeepEqual(before, after) {
						t.Fatal("automatic quota recovery erased an active operator force")
					}
				}
			})
		}
	}
}
