package auth

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func cooldownErrorBody(t *testing.T, err error) map[string]any {
	t.Helper()
	var payload map[string]any
	if errUnmarshal := json.Unmarshal([]byte(err.Error()), &payload); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal(Error()) error = %v; body=%s", errUnmarshal, err.Error())
	}
	body, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("Error() payload missing error object: %v", payload)
	}
	return body
}

func TestModelCooldownErrorLongQuotaExhaustionReportsUsageLimit(t *testing.T) {
	resetIn := 140 * time.Hour
	before := time.Now().Add(resetIn).Unix()
	err := NewQuotaExhaustedModelCooldownError("gemini-4-argon-high", "antigravity", resetIn)
	after := time.Now().Add(resetIn).Unix()

	if !IsUsageLimitReachedError(err) {
		t.Fatal("IsUsageLimitReachedError() = false for a 140h quota-exhausted cooldown")
	}
	mce := err.(*modelCooldownError)
	if mce.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("StatusCode() = %d, want 429", mce.StatusCode())
	}
	headers := mce.Headers()
	if got := headers.Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After = %q, want absent: clients would sleep through the whole window", got)
	}
	if got := headers.Get("X-Should-Retry"); got != "false" {
		t.Fatalf("X-Should-Retry = %q, want false", got)
	}
	if got := SafeResponseHeaders(err).Get("X-Should-Retry"); got != "false" {
		t.Fatalf("SafeResponseHeaders X-Should-Retry = %q, want false", got)
	}

	body := cooldownErrorBody(t, err)
	if got := body["type"]; got != "usage_limit_reached" {
		t.Fatalf("error.type = %v, want usage_limit_reached", got)
	}
	if got := body["code"]; got != "model_cooldown" {
		t.Fatalf("error.code = %v, want model_cooldown for existing consumers", got)
	}
	if got := body["retryable"]; got != false {
		t.Fatalf("error.retryable = %v, want false", got)
	}
	resetsAt, ok := body["resets_at"].(float64)
	if !ok || int64(resetsAt) < before || int64(resetsAt) > after {
		t.Fatalf("error.resets_at = %v, want unix time in [%d, %d]", body["resets_at"], before, after)
	}
	if got := body["reset_seconds"]; got != float64(resetIn/time.Second) {
		t.Fatalf("error.reset_seconds = %v, want %d", got, int(resetIn/time.Second))
	}
	message, _ := body["message"].(string)
	if message == "" || message == "All credentials for model gemini-4-argon-high are cooling down via provider antigravity" {
		t.Fatalf("error.message = %q, want a usage-limit message", message)
	}
}

func TestRestoreModelCooldownErrorModelKeepsQuotaEvidence(t *testing.T) {
	err := restoreModelCooldownErrorModel(NewQuotaExhaustedModelCooldownError("", "codex", 140*time.Hour), "gpt-5.6-sol")
	if !IsUsageLimitReachedError(err) {
		t.Fatalf("restored cooldown lost its quota evidence: %v", err)
	}
}

func TestQuotaExhaustionEvidenceMatchesCanonicalModelSpellings(t *testing.T) {
	now := time.Now()
	auth := &Auth{ModelStates: map[string]*ModelState{
		"QUOTA-MODEL": {
			Unavailable:    true,
			NextRetryAfter: now.Add(time.Hour),
			Quota:          QuotaState{Exceeded: true, Exhausted: true, NextRecoverAt: now.Add(time.Hour)},
		},
	}}
	_, err := getAvailableAuths([]*Auth{auth}, "test", "quota-model", now)
	assertPoolUsageLimit(t, "case-insensitive model state", err, true)
}

func TestQuotaExhaustionEvidenceCoversEffectiveBlockingDeadline(t *testing.T) {
	now := time.Now()
	quota := QuotaState{Exceeded: true, Exhausted: true, NextRecoverAt: now.Add(time.Hour)}
	for _, credentialQuota := range []bool{false, true} {
		auth := &Auth{
			Provider:       "quota-pool",
			Unavailable:    true,
			NextRetryAfter: now.Add(2 * time.Hour),
			Quota:          quota,
			ModelStates: map[string]*ModelState{
				"quota-model": {Unavailable: true, NextRetryAfter: now.Add(2 * time.Hour), Quota: quota},
			},
		}
		if credentialQuota {
			auth.Quota.Reason = "credential_quota"
		} else {
			auth.forceCooldownUntil = now.Add(2 * time.Hour)
		}
		_, err := getAvailableAuths([]*Auth{auth}, auth.Provider, "quota-model", now)
		assertPoolUsageLimit(t, fmt.Sprintf("credentialQuota=%v", credentialQuota), err, false)
	}
}

func TestCooldownQuotaCopyAndComparisonIncludeExhaustion(t *testing.T) {
	quota := QuotaState{Exceeded: true, Exhausted: true, NextRecoverAt: time.Now().Add(time.Hour)}
	copied := cooldownFieldsOf(quota)
	var restored QuotaState
	applyCooldownFields(&restored, copied)
	if !restored.Exhausted || !cooldownQuotaEqual(quota, restored) {
		t.Fatal("cooldown copy lost exhaustion evidence")
	}
	restored.Exhausted = false
	if cooldownQuotaEqual(quota, restored) {
		t.Fatal("cooldown comparison ignored exhaustion evidence")
	}
	applyCooldownFields(&copied, QuotaState{})
	if copied.Exhausted {
		t.Fatal("clearing cooldown retained exhaustion evidence")
	}
}

func TestQuotaCooldownRestorePreservesClaudeMetadataAndDeadline(t *testing.T) {
	original := newModelCooldownErrorForCandidates("", "claude", 140*time.Hour, nil, true, true, map[string]string{
		"Anthropic-Ratelimit-Unified-Status":               "rejected",
		"Anthropic-Ratelimit-Unified-Representative-Claim": "seven_day",
	})
	restored := restoreModelCooldownErrorModel(original, "claude-model").(*modelCooldownError)
	if !restored.resetAt.Equal(original.resetAt) || !restored.IsUsageLimitReached() {
		t.Fatal("restoring model lost deadline or exhaustion evidence")
	}
	headers := restored.Headers()
	if headers.Get("X-Should-Retry") != "false" || headers.Get("Retry-After") != "" {
		t.Fatalf("final quota headers = %v", headers)
	}
	if headers.Get("Anthropic-Ratelimit-Unified-Representative-Claim") != "seven_day" {
		t.Fatalf("restoring model lost Claude metadata: %v", headers)
	}
}

// quotaTestUpstreamError stands in for an executor error. quotaExhausted mirrors
// a structured code such as usage_limit_reached; transient mirrors a Codex
// turn_rate_limit_exceeded admission rejection.
type quotaTestUpstreamError struct {
	quotaExhausted bool
	transient      bool
}

func (e quotaTestUpstreamError) Error() string              { return `{"error":{"message":"rate limited"}}` }
func (e quotaTestUpstreamError) StatusCode() int            { return http.StatusTooManyRequests }
func (e quotaTestUpstreamError) IsQuotaExhausted() bool     { return e.quotaExhausted }
func (e quotaTestUpstreamError) IsTransientRateLimit() bool { return e.transient }

func TestResultErrorFromErrorRecordsOnlyExplicitQuotaExhaustion(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "explicit quota exhaustion", err: quotaTestUpstreamError{quotaExhausted: true}, want: true},
		{name: "plugin quota code", err: &Error{Code: ErrorCodeQuotaExhausted, HTTPStatus: http.StatusTooManyRequests}, want: true},
		{name: "unclassified 429", err: quotaTestUpstreamError{}, want: false},
		{name: "transient capacity", err: quotaTestUpstreamError{transient: true}, want: false},
		{name: "non-429 quota code", err: &Error{Code: ErrorCodeQuotaExhausted, HTTPStatus: http.StatusForbidden}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isQuotaExhaustedResultError(resultErrorFromError(test.err)); got != test.want {
				t.Fatalf("isQuotaExhaustedResultError() = %v, want %v", got, test.want)
			}
		})
	}
}

// quotaMark is one 429 recorded for a pool member. A zero retryAfter sends no
// provider hint, so the manager may reuse a live deadline.
type quotaMark struct {
	exhausted       bool
	transient       bool
	credentialScope bool
	retryAfter      time.Duration
}

// quotaPoolCooldownErrors registers one auth per member, records its marks in
// order for one model and returns the pool error from the scheduler, manager
// and built-in selector paths.
func quotaPoolCooldownErrors(t *testing.T, members ...[]quotaMark) map[string]error {
	t.Helper()
	const model = "quota-pool-model"
	provider := "quota-pool-" + uuid.NewString()
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(&refreshMockExecutor{id: provider})
	reg := registry.GetGlobalRegistry()
	auths := make([]*Auth, 0, len(members))
	for _, marks := range members {
		authID := provider + "-" + uuid.NewString()
		reg.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { reg.UnregisterClient(authID) })
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: authID, Provider: provider}); errRegister != nil {
			t.Fatalf("Register() error = %v", errRegister)
		}
		for _, mark := range marks {
			var retryAfter *time.Duration
			if mark.retryAfter > 0 {
				retryAfter = &mark.retryAfter
			}
			manager.MarkResult(context.Background(), Result{
				AuthID:          authID,
				Provider:        provider,
				Model:           model,
				RetryAfter:      retryAfter,
				CredentialScope: mark.credentialScope,
				Error:           resultErrorFromError(quotaTestUpstreamError{quotaExhausted: mark.exhausted, transient: mark.transient}),
			})
		}
		auth, ok := manager.GetByID(authID)
		if !ok {
			t.Fatalf("GetByID(%q) missing", authID)
		}
		auths = append(auths, auth)
	}
	now := time.Now()
	_, _, errScheduler := manager.pickNext(context.Background(), provider, model, cliproxyexecutor.Options{}, nil)
	_, errManager := manager.availableAuthsForRouteModel(auths, provider, model, now)
	_, errSelector := getAvailableAuths(auths, provider, model, now)
	return map[string]error{"scheduler": errScheduler, "manager": errManager, "selector": errSelector}
}

func assertPoolUsageLimit(t *testing.T, label string, err error, want bool) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: error = nil, want a pool cooldown", label)
		return
	}
	headers := SafeResponseHeaders(err)
	if want {
		if !IsUsageLimitReachedError(err) || headers.Get("Retry-After") != "" || headers.Get("X-Should-Retry") != "false" {
			t.Errorf("%s: want a final usage limit without Retry-After; headers=%v", label, headers)
		}
		return
	}
	if IsUsageLimitReachedError(err) || headers.Get("Retry-After") == "" || headers.Get("X-Should-Retry") != "" {
		t.Errorf("%s: want a retryable cooldown with Retry-After; headers=%v", label, headers)
	}
}

func TestPoolCooldownRequiresEvidenceForEveryCandidateDeadline(t *testing.T) {
	long, longer, short := 140*time.Hour, 150*time.Hour, time.Minute
	tests := []struct {
		name    string
		members [][]quotaMark
		want    bool
	}{
		{name: "unclassified long 429", members: [][]quotaMark{{{retryAfter: long}}, {{retryAfter: longer}}}},
		{name: "mixed exhausted and unclassified", members: [][]quotaMark{{{exhausted: true, retryAfter: long}}, {{retryAfter: 2 * time.Hour}}}},
		{name: "mixed exhausted and transient", members: [][]quotaMark{{{exhausted: true, retryAfter: long}}, {{transient: true, retryAfter: 2 * time.Hour}}}},
		{name: "short evidence after a long unclassified 429", members: [][]quotaMark{{{retryAfter: long}, {exhausted: true, retryAfter: short}}}},
		{name: "hintless evidence reusing an unclassified deadline", members: [][]quotaMark{{{retryAfter: long}, {exhausted: true}}}},
		{name: "all exhausted", members: [][]quotaMark{{{exhausted: true, retryAfter: long}}, {{exhausted: true, retryAfter: longer}}}, want: true},
		{name: "long evidence after a short unclassified 429", members: [][]quotaMark{{{retryAfter: short}, {exhausted: true, retryAfter: long}}}, want: true},
	}
	for _, credentialScope := range []bool{false, true} {
		for _, test := range tests {
			for _, marks := range test.members {
				for i := range marks {
					marks[i].credentialScope = credentialScope && !marks[i].transient
				}
			}
			for path, err := range quotaPoolCooldownErrors(t, test.members...) {
				assertPoolUsageLimit(t, fmt.Sprintf("%s (credentialScope=%v, %s path)", test.name, credentialScope, path), err, test.want)
			}
		}
	}
}

func TestAuthLevelQuotaEvidenceFollowsDeadline(t *testing.T) {
	now := time.Now()
	long, short := 140*time.Hour, time.Minute
	unclassified := resultErrorFromError(quotaTestUpstreamError{})
	exhausted := resultErrorFromError(quotaTestUpstreamError{quotaExhausted: true})

	auth := &Auth{ID: "auth-level-quota", Provider: "quota-pool"}
	applyAuthFailureState(auth, unclassified, &long, now, false)
	applyAuthFailureState(auth, exhausted, &short, now, false)
	_, err := getAvailableAuths([]*Auth{auth}, auth.Provider, "", now)
	assertPoolUsageLimit(t, "short evidence after a long unclassified 429", err, false)
	applyAuthFailureState(auth, exhausted, nil, now, false)
	_, err = getAvailableAuths([]*Auth{auth}, auth.Provider, "", now)
	assertPoolUsageLimit(t, "hintless evidence reusing an unclassified deadline", err, false)

	fresh := &Auth{ID: "auth-level-exhausted", Provider: "quota-pool"}
	applyAuthFailureState(fresh, exhausted, &long, now, false)
	_, err = getAvailableAuths([]*Auth{fresh}, fresh.Provider, "", now)
	assertPoolUsageLimit(t, "long explicit exhaustion", err, true)
}

func TestMergedAndRestoredStateNeedsEvidenceForBlockingDeadline(t *testing.T) {
	const model = "quota-pool-model"
	now := time.Now()
	exhausted := &ModelState{
		Status:         StatusError,
		Unavailable:    true,
		NextRetryAfter: now.Add(time.Minute),
		Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(time.Minute), Exhausted: true},
		UpdatedAt:      now,
	}
	unclassified := &ModelState{Status: StatusError, Unavailable: true, NextRetryAfter: now.Add(140 * time.Hour), UpdatedAt: now.Add(-time.Second)}
	merged := &Auth{ID: "merged-quota", Provider: "quota-pool", ModelStates: map[string]*ModelState{model: mergeModelState(exhausted, unclassified)}}
	data, errMarshal := json.Marshal(merged)
	if errMarshal != nil {
		t.Fatalf("json.Marshal() error = %v", errMarshal)
	}
	restored := &Auth{}
	if errUnmarshal := json.Unmarshal(data, restored); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	for name, auth := range map[string]*Auth{"merged": merged, "restored": restored} {
		_, err := getAvailableAuths([]*Auth{auth}, auth.Provider, model, now)
		assertPoolUsageLimit(t, name+" 140h unclassified block over 1m evidence", err, false)
	}
}

type quotaCredentialTestError struct {
	quotaTestUpstreamError
	code string
}

func (e quotaCredentialTestError) IsCredentialScoped() bool { return true }
func (e quotaCredentialTestError) ErrorCode() string        { return e.code }

func TestResultErrorFromErrorPreservesCredentialQuotaAndExhaustion(t *testing.T) {
	for _, test := range []struct {
		name      string
		code      string
		exhausted bool
	}{
		{name: "default credential quota", exhausted: true},
		{name: "explicit credential code", code: credentialQuotaErrorCode, exhausted: true},
		{name: "force cooldown code", code: ErrorCodeForceCooldown, exhausted: true},
		{name: "provider code", code: "provider_quota", exhausted: true},
		{name: "unclassified credential quota", code: credentialQuotaErrorCode},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := quotaCredentialTestError{
				quotaTestUpstreamError: quotaTestUpstreamError{quotaExhausted: test.exhausted},
				code:                   test.code,
			}
			recorded := resultErrorFromError(fmt.Errorf("wrapped: %w", raw))
			wantCode := test.code
			if wantCode == "" && test.exhausted {
				wantCode = credentialQuotaErrorCode
			}
			if recorded.Code != wantCode {
				t.Fatalf("recorded code = %q, want %q", recorded.Code, wantCode)
			}
			if isQuotaExhaustedResultError(recorded) != test.exhausted {
				t.Fatalf("recorded exhaustion = %v, want %v", recorded.IsQuotaExhausted(), test.exhausted)
			}
			if wantCode == credentialQuotaErrorCode && !isCredentialScopedError(recorded) {
				t.Fatal("recorded credential quota lost its scope")
			}
			encoded, err := json.Marshal(recorded)
			if err != nil {
				t.Fatal(err)
			}
			var restored Error
			if err := json.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			again := resultErrorFromError(&restored)
			if again.Code != wantCode || again.IsQuotaExhausted() != test.exhausted {
				t.Fatalf("recorded error lost classification across JSON and reconversion: %+v", again)
			}
			cloned := recorded.Clone()
			if test.exhausted {
				cloned.Metadata[quotaExhaustedMetadataKey] = "false"
				if !recorded.IsQuotaExhausted() {
					t.Fatal("cloned error shared mutable quota evidence")
				}
			}
		})
	}
}

// The Codex continuity hook may hold a request for a bounded wait before the
// pool cooldown answer, but it does not change what the pool reports: an
// explicitly exhausted pool is still a final usage limit when the hook gives
// up, and a pool without that evidence still carries Retry-After.
func TestContinuityGateKeepsPoolCooldownRetryGuidance(t *testing.T) {
	const model = "quota-continuity-model"
	tests := []struct {
		name      string
		exhausted bool
		wait      bool
	}{
		{name: "exhausted pool after the wait budget", exhausted: true, wait: true},
		{name: "exhausted pool declined by the plugin", exhausted: true},
		{name: "unclassified pool after the wait budget", wait: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			manager.SetContinuityClockForTest(newFakeContinuityClock(time.Now()))
			manager.SetRetryConfig(3, 0, 0)
			manager.RegisterExecutor(&continuityTestExecutor{provider: "codex"})
			authID := "quota-continuity-" + uuid.NewString()
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { reg.UnregisterClient(authID) })
			if _, errRegister := manager.Register(context.Background(), &Auth{ID: authID, Provider: "codex", Status: StatusActive}); errRegister != nil {
				t.Fatalf("Register() error = %v", errRegister)
			}
			resetIn := time.Hour
			manager.MarkResult(context.Background(), Result{
				AuthID:     authID,
				Provider:   "codex",
				Model:      model,
				RetryAfter: &resetIn,
				Error:      resultErrorFromError(quotaTestUpstreamError{quotaExhausted: test.exhausted}),
			})

			polls := 0
			manager.SetPluginScheduler(&testContinuityScheduler{
				hasContinuity: true,
				continuityFunc: func(_ context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
					polls++
					if len(req.Candidates) != 1 || !req.Candidates[0].InCooldown || req.Candidates[0].CooldownUntil == nil {
						t.Errorf("candidates = %#v, want the cooling credential with its recovery time", req.Candidates)
					}
					if !test.wait {
						return pluginapi.SchedulerContinuityResponse{}, false, nil
					}
					return pluginapi.SchedulerContinuityResponse{
						Decision:       pluginapi.SchedulerContinuityWait,
						PollIntervalMs: 500,
						WaitBudgetMs:   2000,
					}, true, nil
				},
			})

			_, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			if polls == 0 {
				t.Fatal("continuity gate was not consulted")
			}
			if !IsContinuityTerminatedError(errExecute) {
				t.Fatalf("Execute() error = %v, want the pool cooldown ended by the continuity gate", errExecute)
			}
			assertPoolUsageLimit(t, test.name, errExecute, test.exhausted)
		})
	}
}
