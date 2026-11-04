package auth

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func credentialQuotaAuth(id string, exhausted bool, recoverAt time.Time, window int64) *Auth {
	return &Auth{
		ID:             id,
		Provider:       "codex",
		Status:         StatusError,
		Unavailable:    true,
		Quota:          QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: recoverAt, Exhausted: exhausted, LimitWindowMinutes: window},
		NextRetryAfter: recoverAt,
	}
}

// usageLimitManager registers auths for provider "codex" and model "gpt-5".
func usageLimitManager(t *testing.T, auths ...*Auth) *Manager {
	t.Helper()
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(schedulerTestExecutor{provider: "codex"})
	for _, auth := range auths {
		if _, err := m.Register(WithSkipPersist(context.Background()), auth); err != nil {
			t.Fatal(err)
		}
		registryRegister(t, auth.ID, auth.Provider, "gpt-5")
	}
	return m
}

func registryRegister(t *testing.T, authID, provider, model string) {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })
}

func poolRefusal(m *Manager, now time.Time) *usageLimitRefusal {
	return m.usageLimitRefusalForPool([]string{"codex"}, "gpt-5", authSelectionEligibility{}, "", now)
}

func TestUsageLimitRefusalForPool(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name      string
		auths     []*Auth
		wantReset time.Time
		window    int64
	}{
		{name: "exactly five minutes stays retryable", auths: []*Auth{credentialQuotaAuth("a", true, now.Add(usageLimitMinResetIn), 300)}},
		{name: "just over five minutes is a usage limit", auths: []*Auth{credentialQuotaAuth("a", true, now.Add(usageLimitMinResetIn+time.Second), 300)}, wantReset: now.Add(usageLimitMinResetIn + time.Second), window: 300},
		{name: "earliest reset wins with its window", auths: []*Auth{credentialQuotaAuth("a", true, now.Add(72*time.Hour), 10080), credentialQuotaAuth("b", true, now.Add(2*time.Hour), 300)}, wantReset: now.Add(2 * time.Hour), window: 300},
		{name: "status-disabled candidate is not eligible", auths: []*Auth{credentialQuotaAuth("a", true, now.Add(2*time.Hour), 300), {ID: "b", Provider: "codex", Status: StatusDisabled}}, wantReset: now.Add(2 * time.Hour), window: 300},
		{name: "candidate with the model disabled is not eligible", auths: []*Auth{credentialQuotaAuth("a", true, now.Add(2*time.Hour), 300), {ID: "b", Provider: "codex", Status: StatusActive, ModelStates: map[string]*ModelState{"gpt-5": {Status: StatusDisabled}}}}, wantReset: now.Add(2 * time.Hour), window: 300},
		{name: "available candidate stays retryable", auths: []*Auth{credentialQuotaAuth("a", true, now.Add(2*time.Hour), 300), {ID: "b", Provider: "codex", Status: StatusActive}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refusal := poolRefusal(usageLimitManager(t, tc.auths...), now)
			if tc.wantReset.IsZero() != (refusal == nil) || (refusal != nil && (!refusal.resetAt.Equal(tc.wantReset) || refusal.windowMinutes != tc.window)) {
				t.Fatalf("refusal = %+v, want reset %v window %d", refusal, tc.wantReset, tc.window)
			}
		})
	}
}

func TestQuotaExhaustionEvidenceMustCoverModelDeadline(t *testing.T) {
	now := time.Now()
	recover := now.Add(2 * time.Hour)
	state := &ModelState{Unavailable: true, Status: StatusError, NextRetryAfter: recover, Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recover, Exhausted: true, LimitWindowMinutes: 300}}
	auth := &Auth{ID: "a", Provider: "claude", Status: StatusActive, ModelStates: map[string]*ModelState{"claude-opus": state}}
	if window, ok := quotaExhaustionEvidence(auth, "claude-opus", now); !ok || window != 300 {
		t.Fatalf("evidence = (%d, %t), want (300, true)", window, ok)
	}
	// A later deadline set by something else is not covered by the evidence.
	state.NextRetryAfter = recover.Add(time.Hour)
	if _, ok := quotaExhaustionEvidence(auth, "claude-opus", now); ok {
		t.Fatal("evidence covered a longer deadline it did not set")
	}
}

// Evidence is recorded only with a provider reset, persists through the
// cooldown store, and is cleared with the cooldown.
func TestMarkResultQuotaEvidence(t *testing.T) {
	reset := 2 * time.Hour
	for _, tc := range []struct {
		name            string
		retryAfter      *time.Duration
		credentialScope bool
		want            bool
	}{
		{name: "model scope with reset", retryAfter: &reset, want: true},
		{name: "credential scope with reset", retryAfter: &reset, credentialScope: true, want: true},
		{name: "no reset", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "auths")
			newManager := func() *Manager {
				m := usageLimitManager(t, &Auth{ID: "auth-evidence", Provider: "codex", Status: StatusActive})
				m.SetCooldownStateStore(NewFileCooldownStateStore(dir))
				return m
			}
			before := newManager()
			before.MarkResult(context.Background(), Result{
				AuthID: "auth-evidence", Provider: "codex", Model: "gpt-5",
				Error:      &Error{Message: "usage_limit_reached", HTTPStatus: http.StatusTooManyRequests},
				RetryAfter: tc.retryAfter, QuotaExhausted: true, QuotaLimitWindowMinutes: 300, CredentialScope: tc.credentialScope,
			})
			before.PersistCooldownStates(context.Background())
			after := newManager()
			if err := after.RestoreCooldownStates(context.Background()); err != nil {
				t.Fatal(err)
			}
			for name, m := range map[string]*Manager{"live": before, "restored": after} {
				refusal := poolRefusal(m, time.Now())
				if (refusal != nil) != tc.want || (refusal != nil && (refusal.windowMinutes != 300 || time.Until(refusal.resetAt) < reset-time.Minute)) {
					t.Fatalf("%s: refusal = %+v, want usage limit %t", name, refusal, tc.want)
				}
			}

			before.MarkResult(context.Background(), Result{AuthID: "auth-evidence", Provider: "codex", Model: "gpt-5", Success: true})
			got, _ := before.GetByID("auth-evidence")
			quotas := []QuotaState{got.Quota}
			for _, state := range got.ModelStates {
				quotas = append(quotas, state.Quota)
			}
			for _, quota := range quotas {
				if quota.Exhausted && !quota.Exceeded {
					t.Fatalf("evidence outlived its cooldown: %+v", quota)
				}
			}
		})
	}
}

func TestUsageLimitHeadersNameOnlyKnownCodexWindows(t *testing.T) {
	resetAt := time.Unix(1793766340, 0)
	for window, want := range map[int64]string{300: "Primary", 10080: "Secondary", 0: ""} {
		headers := (&usageLimitRefusal{resetAt: resetAt, windowMinutes: window}).headers()
		for _, slot := range []string{"Primary", "Secondary"} {
			wantUsed, wantReset := "", ""
			if slot == want {
				wantUsed, wantReset = "100", "1793766340"
			}
			if headers.Get("X-Codex-"+slot+"-Used-Percent") != wantUsed || headers.Get("X-Codex-"+slot+"-Reset-At") != wantReset {
				t.Fatalf("window %d: headers = %v, want %s slot only", window, headers, want)
			}
		}
	}
}

type quotaResetOnlyErr struct{}

func (quotaResetOnlyErr) Error() string                   { return "quota exhausted" }
func (quotaResetOnlyErr) StatusCode() int                 { return http.StatusTooManyRequests }
func (quotaResetOnlyErr) IsQuotaExhausted() bool          { return true }
func (quotaResetOnlyErr) QuotaLimitWindowMinutes() int64  { return 0 }
func (quotaResetOnlyErr) QuotaResetAfter() *time.Duration { d := 2 * time.Hour; return &d }

// A quota reset reported apart from the retry hint (Gemini and Vertex) sets
// the cooldown only in the usage-limit mode; the default keeps its backoff.
func TestQuotaResetWithoutRetryHintAppliesOnlyInUsageLimitMode(t *testing.T) {
	t.Cleanup(func() { SetQuotaCooldownResponseMode("") })
	for _, mode := range []string{"retryable", "usage-limit"} {
		t.Run(mode, func(t *testing.T) {
			SetQuotaCooldownResponseMode(mode)
			manager := usageLimitManager(t, &Auth{ID: "auth-reset", Provider: "codex", Status: StatusActive})
			result := Result{AuthID: "auth-reset", Provider: "codex", Model: "gpt-5", Error: &Error{Message: "quota exhausted", HTTPStatus: http.StatusTooManyRequests}}
			result.attachQuotaEvidence(quotaResetOnlyErr{})
			manager.MarkResult(context.Background(), result)
			got, _ := manager.GetByID("auth-reset")
			state := got.ModelStates["gpt-5"]
			wait := time.Until(state.NextRetryAfter)
			if mode == "retryable" && (state.Quota.Exhausted || wait > time.Hour) {
				t.Fatalf("default mode changed: exhausted=%t cooldown=%v", state.Quota.Exhausted, wait)
			}
			if mode == "usage-limit" && (!state.Quota.Exhausted || wait < 2*time.Hour-time.Minute) {
				t.Fatalf("usage-limit mode: exhausted=%t cooldown=%v, want evidence and about 2h", state.Quota.Exhausted, wait)
			}
		})
	}
}

// poolQuotaErr is an explicit upstream quota exhaustion with a 2h reset.
type poolQuotaErr struct{}

func (poolQuotaErr) Error() string                   { return `{"error":{"code":"insufficient_quota"}}` }
func (poolQuotaErr) StatusCode() int                 { return http.StatusTooManyRequests }
func (poolQuotaErr) RetryAfter() *time.Duration      { d := 2 * time.Hour; return &d }
func (poolQuotaErr) IsQuotaExhausted() bool          { return true }
func (poolQuotaErr) QuotaLimitWindowMinutes() int64  { return 0 }
func (poolQuotaErr) QuotaResetAfter() *time.Duration { return nil }

// An alias pooled over several backends records cooldowns under the backend
// model names: the usage limit needs every backend out of quota, both when the
// last backend refuses and when all are already cooling.
func TestUsageLimitModeOpenAICompatAliasPool(t *testing.T) {
	SetQuotaCooldownResponseMode("usage-limit")
	t.Cleanup(func() { SetQuotaCooldownResponseMode("") })
	for _, path := range []string{"execute", "stream", "count"} {
		failed := map[string]error{"backend-a": poolQuotaErr{}, "backend-b": poolQuotaErr{}}
		executor := &openAICompatPoolExecutor{id: openAICompatPoolProviderKey, executeErrors: failed, streamFirstErrors: failed, countErrors: failed}
		m := newOpenAICompatPoolTestManager(t, "public", []internalconfig.OpenAICompatibilityModel{
			{Name: "backend-a", Alias: "public"},
			{Name: "backend-b", Alias: "public"},
		}, executor)
		for request := 0; request < 2; request++ {
			var err error
			req, opts := cliproxyexecutor.Request{Model: "public"}, cliproxyexecutor.Options{Stream: path == "stream"}
			switch path {
			case "stream":
				var result *cliproxyexecutor.StreamResult
				if result, err = m.ExecuteStream(context.Background(), []string{openAICompatPoolProviderKey}, req, opts); err == nil {
					for chunk := range result.Chunks {
						err = chunk.Err
					}
				}
			case "count":
				_, err = m.ExecuteCount(context.Background(), []string{openAICompatPoolProviderKey}, req, opts)
			default:
				_, err = m.Execute(context.Background(), []string{openAICompatPoolProviderKey}, req, opts)
			}
			if resetAt, ok := UsageLimitResetAt(err); !ok || time.Until(resetAt) < 2*time.Hour-time.Minute {
				t.Fatalf("%s request %d: err = %v, want a usage limit", path, request, err)
			}
		}
		// One backend that can serve again keeps the retryable answer.
		auth := m.auths["pool-auth-"+t.Name()]
		delete(auth.ModelStates, m.stateModelForExecution(auth, "public", "backend-b", true))
		if refusal := m.usageLimitRefusalForPool([]string{openAICompatPoolProviderKey}, "public", authSelectionEligibility{}, "", time.Now()); refusal != nil {
			t.Fatalf("%s: refusal = %+v with backend-b available", path, refusal)
		}
	}
}

// Evidence belongs to the failure that set the kept deadline: a short explicit
// exhaustion does not vouch for a longer generic cooldown already in place.
func TestQuotaEvidenceFollowsKeptDeadline(t *testing.T) {
	m := usageLimitManager(t, &Auth{ID: "auth-kept", Provider: "codex", Status: StatusActive})
	long, short := 2*time.Hour, 10*time.Minute
	for _, result := range []Result{{RetryAfter: &long}, {RetryAfter: &short, QuotaExhausted: true}} {
		result.AuthID, result.Provider, result.Model = "auth-kept", "codex", "gpt-5"
		result.Error = &Error{Message: "limited", HTTPStatus: http.StatusTooManyRequests}
		m.MarkResult(context.Background(), result)
	}
	if refusal := poolRefusal(m, time.Now()); refusal != nil {
		t.Fatalf("refusal = %+v, want none for a generic cooldown", refusal)
	}
}

// A later 429 without a reset hint keeps the live deadline, and so keeps the
// evidence of the exhaustion that set it.
func TestQuotaEvidenceSurvivesReusedDeadline(t *testing.T) {
	for _, credentialScope := range []bool{false, true} {
		m := usageLimitManager(t, &Auth{ID: "auth-reused", Provider: "codex", Status: StatusActive})
		reset := 2 * time.Hour
		for _, result := range []Result{{RetryAfter: &reset, QuotaExhausted: true, QuotaLimitWindowMinutes: 300}, {}} {
			result.AuthID, result.Provider, result.Model, result.CredentialScope = "auth-reused", "codex", "gpt-5", credentialScope
			result.Error = &Error{Message: "limited", HTTPStatus: http.StatusTooManyRequests}
			m.MarkResult(context.Background(), result)
		}
		if refusal := poolRefusal(m, time.Now()); refusal == nil || refusal.windowMinutes != 300 {
			t.Fatalf("credential scope %t: refusal = %+v, want the usage limit kept", credentialScope, refusal)
		}
	}
}

// Weighted round-robin never picks a zero-weight credential, so it does not
// keep the pool retryable; other selectors can still pick it.
func TestUsageLimitRefusalSkipsZeroWeightUnderWeightedSelection(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name     string
		selector Selector
		want     bool
	}{
		{name: "weighted", selector: &WeightedRoundRobinSelector{}, want: true},
		{name: "session affinity over weighted", selector: NewSessionAffinitySelector(&WeightedRoundRobinSelector{}), want: true},
		{name: "round robin", selector: &RoundRobinSelector{}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			zero := &Auth{ID: "zero-weight", Provider: "codex", Status: StatusActive, Attributes: map[string]string{AttributeWeight: "0"}}
			m := usageLimitManager(t, credentialQuotaAuth("exhausted", true, now.Add(2*time.Hour), 300), zero)
			m.SetSelector(tc.selector)
			if refusal := poolRefusal(m, now); (refusal != nil) != tc.want {
				t.Fatalf("refusal = %+v, want usage limit %t", refusal, tc.want)
			}
		})
	}
}
