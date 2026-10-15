package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
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
	err := restoreModelCooldownErrorModel(newModelCooldownError("", "codex", 140*time.Hour, true), "gpt-5.6-sol")
	if !IsUsageLimitReachedError(err) {
		t.Fatalf("restored cooldown lost its quota evidence: %v", err)
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
