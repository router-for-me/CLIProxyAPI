package auth

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const requestScopedNotFoundMessage = "Item with id 'rs_0b5f3eb6f51f175c0169ca74e4a85881998539920821603a74' not found. Items are not persisted when `store` is set to false. Try again with `store` set to true, or remove this item from your input."
const requestScopedContentSafetyMessage = "The request was rejected because it was considered high risk"
const requestScopedContentBlockedMessage = "The content you provided or machine outputted is blocked."
const requestScopedContextLimitMessage = "invalid params, context window exceeds limit (2013)"
const miniMaxNewSensitiveMessage = "server_error: input new_sensitive, messages[2]'s content[1] image is sensitive, please check your input (1026)"
const miniMaxOutputNewSensitiveMessage = "server_error: output new_sensitive, generated content is sensitive, please check your input (1027)"
const miniMaxUnknown1000Message = "server_error: unknown error, 999 (1000)"

func TestManager_ShouldRetryAfterError_RespectsAuthRequestRetryOverride(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(3, 30*time.Second, 0)

	model := "test-model"
	next := time.Now().Add(5 * time.Second)

	auth := &Auth{
		ID:       "auth-1",
		Provider: "claude",
		Metadata: map[string]any{
			"request_retry": float64(0),
		},
		ModelStates: map[string]*ModelState{
			model: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: next,
			},
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	_, _, maxWait := m.retrySettings()
	wait, shouldRetry := m.shouldRetryAfterError(&Error{HTTPStatus: 500, Message: "boom"}, 0, []string{"claude"}, model, maxWait)
	if shouldRetry {
		t.Fatalf("expected shouldRetry=false for request_retry=0, got true (wait=%v)", wait)
	}

	auth.Metadata["request_retry"] = float64(1)
	if _, errUpdate := m.Update(context.Background(), auth); errUpdate != nil {
		t.Fatalf("update auth: %v", errUpdate)
	}

	wait, shouldRetry = m.shouldRetryAfterError(&Error{HTTPStatus: 500, Message: "boom"}, 0, []string{"claude"}, model, maxWait)
	if !shouldRetry {
		t.Fatalf("expected shouldRetry=true for request_retry=1, got false")
	}
	if wait <= 0 {
		t.Fatalf("expected wait > 0, got %v", wait)
	}

	_, shouldRetry = m.shouldRetryAfterError(&Error{HTTPStatus: 500, Message: "boom"}, 1, []string{"claude"}, model, maxWait)
	if shouldRetry {
		t.Fatalf("expected shouldRetry=false on attempt=1 for request_retry=1, got true")
	}
}

func TestManager_ShouldRetryAfterError_EmptyUpstreamResponseUsesConfiguredRetry(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(3, 30*time.Second, 0)

	model := "gpt-5.5"
	auth := &Auth{
		ID:       "empty-upstream-auth",
		Provider: "codex",
		Metadata: map[string]any{
			"request_retry": float64(0),
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	errEmpty := codedStatusError{
		status: http.StatusInternalServerError,
		code:   emptyUpstreamResponseErrorCode,
		msg:    "empty upstream response",
	}
	_, _, maxWait := m.retrySettings()
	if wait, shouldRetry := m.shouldRetryAfterError(errEmpty, 0, []string{"codex"}, model, maxWait); shouldRetry {
		t.Fatalf("expected shouldRetry=false for request_retry=0, got true (wait=%v)", wait)
	}

	auth.Metadata["request_retry"] = float64(1)
	if _, errUpdate := m.Update(context.Background(), auth); errUpdate != nil {
		t.Fatalf("update auth: %v", errUpdate)
	}

	wait, shouldRetry := m.shouldRetryAfterError(errEmpty, 0, []string{"codex"}, model, maxWait)
	if !shouldRetry {
		t.Fatalf("expected shouldRetry=true for request_retry=1")
	}
	if wait != time.Second {
		t.Fatalf("wait = %v, want %v", wait, time.Second)
	}
	if _, shouldRetry = m.shouldRetryAfterError(errEmpty, 1, []string{"codex"}, model, maxWait); shouldRetry {
		t.Fatalf("expected shouldRetry=false on attempt=1 for request_retry=1")
	}
}

func TestManager_ShouldRetryAfterError_CodexModelCooldownUsesRetryAfter(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(3, 30*time.Second, 0)

	model := "gpt-5.5"
	auth := &Auth{
		ID:       "codex-model-cooldown-auth",
		Provider: "codex",
		Metadata: map[string]any{
			"request_retry": float64(1),
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	_, _, maxWait := m.retrySettings()
	wait, shouldRetry := m.shouldRetryAfterError(newModelCooldownError(model, "codex", 5*time.Second), 0, []string{"codex"}, model, maxWait)
	if !shouldRetry {
		t.Fatalf("expected shouldRetry=true for codex model cooldown")
	}
	if wait != 5*time.Second {
		t.Fatalf("wait = %v, want %v", wait, 5*time.Second)
	}

	if _, shouldRetry = m.shouldRetryAfterError(newModelCooldownError(model, "codex", 5*time.Second), 1, []string{"codex"}, model, maxWait); shouldRetry {
		t.Fatalf("expected shouldRetry=false on attempt=1 for request_retry=1")
	}

	if wait, shouldRetry = m.shouldRetryAfterError(newModelCooldownError(model, "codex", 31*time.Second), 0, []string{"codex"}, model, maxWait); shouldRetry {
		t.Fatalf("expected shouldRetry=false when retry-after exceeds max wait, got wait=%v", wait)
	}
}

func TestManager_ShouldRetryAfterError_UsesOAuthModelAliasForCooldown(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(3, 30*time.Second, 0)
	m.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"kimi": {
			{Name: "deepseek-v3.1", Alias: "pool-model"},
		},
	})

	routeModel := "pool-model"
	upstreamModel := "deepseek-v3.1"
	next := time.Now().Add(5 * time.Second)

	auth := &Auth{
		ID:       "auth-1",
		Provider: "kimi",
		ModelStates: map[string]*ModelState{
			upstreamModel: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: next,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: next,
				},
			},
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	_, _, maxWait := m.retrySettings()
	wait, shouldRetry := m.shouldRetryAfterError(&Error{HTTPStatus: 429, Message: "quota"}, 0, []string{"kimi"}, routeModel, maxWait)
	if !shouldRetry {
		t.Fatalf("expected shouldRetry=true, got false (wait=%v)", wait)
	}
	if wait <= 0 {
		t.Fatalf("expected wait > 0, got %v", wait)
	}
}

func TestManager_ShouldRetryAfterError_TransientNetworkUsesBoundedShortBackoff(t *testing.T) {
	m := NewManager(nil, nil, nil)
	err := &Error{
		HTTPStatus: http.StatusInternalServerError,
		Message:    "read tcp 10.0.0.1:52886->10.0.0.2:443: read: connection reset by peer",
	}

	for attempt, want := range []time.Duration{time.Second, 2 * time.Second, 3 * time.Second} {
		wait, shouldRetry := m.shouldRetryAfterError(err, attempt, []string{"claude"}, "claude-sonnet-4-6", 10*time.Second)
		if !shouldRetry {
			t.Fatalf("attempt %d should retry", attempt)
		}
		if wait != want {
			t.Fatalf("attempt %d wait = %v, want %v", attempt, wait, want)
		}
	}
	if wait, shouldRetry := m.shouldRetryAfterError(err, transientNetworkRetryAttempts, []string{"claude"}, "claude-sonnet-4-6", 10*time.Second); shouldRetry {
		t.Fatalf("attempt %d should stop retrying, got wait %v", transientNetworkRetryAttempts, wait)
	}
}

func TestManager_ShouldRetryAfterError_MiniMaxUnknown1000UsesBoundedShortBackoff(t *testing.T) {
	m := NewManager(nil, nil, nil)
	err := &Error{
		Code:       "server_error",
		HTTPStatus: http.StatusInternalServerError,
		Message:    miniMaxUnknown1000Message,
	}

	for attempt, want := range []time.Duration{time.Second, 2 * time.Second, 3 * time.Second} {
		wait, shouldRetry := m.shouldRetryAfterError(err, attempt, []string{"minimax"}, "MiniMax-M2.7-highspeed", 10*time.Second)
		if !shouldRetry {
			t.Fatalf("attempt %d should retry", attempt)
		}
		if wait != want {
			t.Fatalf("attempt %d wait = %v, want %v", attempt, wait, want)
		}
	}
	if wait, shouldRetry := m.shouldRetryAfterError(err, transientNetworkRetryAttempts, []string{"minimax"}, "MiniMax-M2.7-highspeed", 10*time.Second); shouldRetry {
		t.Fatalf("attempt %d should stop retrying, got wait %v", transientNetworkRetryAttempts, wait)
	}
}

func TestTransientNetworkErrorPatterns(t *testing.T) {
	tests := []string{
		"status_code=500, read tcp 10.0.0.1:52886->10.0.0.2:443: read: connection reset by peer",
		"status_code=500, write tcp 10.0.0.1:52886->10.0.0.2:443: write: broken pipe",
		"unexpected EOF",
		"i/o timeout",
		"context deadline exceeded",
	}
	for _, message := range tests {
		t.Run(message, func(t *testing.T) {
			if !isTransientNetworkError(&Error{HTTPStatus: http.StatusInternalServerError, Message: message}) {
				t.Fatalf("expected transient network error for %q", message)
			}
		})
	}
}

func TestManager_AuthSupportsRouteModel_EmptyRegistryForAPIKeyAuthDoesNotMatch(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "claude-apikey-empty-registry",
		Provider: "claude",
		Attributes: map[string]string{
			"auth_kind": "apikey",
		},
	}

	reg := registry.GetGlobalRegistry()
	reg.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	if got := m.authSupportsRouteModel(reg, auth, "qwen3-coder-plus"); got {
		t.Fatal("expected api key auth without registered models to be rejected")
	}
}

func TestManager_AuthSupportsRouteModel_EmptyRegistryForOAuthAuthStillMatches(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "claude-oauth-empty-registry",
		Provider: "claude",
		Metadata: map[string]any{
			"type": "claude",
		},
	}

	reg := registry.GetGlobalRegistry()
	reg.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	if got := m.authSupportsRouteModel(reg, auth, "claude-sonnet-4-6"); !got {
		t.Fatal("expected oauth auth without registered models to preserve legacy support-all behavior")
	}
}

func TestManager_ShouldRetryAfterError_SequentialFillUsesConfiguredRequestRetry(t *testing.T) {
	m := NewManager(nil, &SequentialFillSelector{}, nil)
	m.SetRetryConfig(5, 30*time.Second, 0)

	model := "test-model"
	next := time.Now().Add(5 * time.Second)

	auth := &Auth{
		ID:       "auth-1",
		Provider: "claude",
		ModelStates: map[string]*ModelState{
			model: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: next,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: next,
				},
			},
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	_, _, maxWait := m.retrySettings()
	wait, shouldRetry := m.shouldRetryAfterError(&Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}, 4, []string{"claude"}, model, maxWait)
	if !shouldRetry {
		t.Fatalf("expected shouldRetry=true on attempt=4 when request_retry=5, got false (wait=%v)", wait)
	}
	if wait <= 0 {
		t.Fatalf("expected wait > 0, got %v", wait)
	}

	_, shouldRetry = m.shouldRetryAfterError(&Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}, 5, []string{"claude"}, model, maxWait)
	if shouldRetry {
		t.Fatalf("expected shouldRetry=false on attempt=5 when request_retry=5, got true")
	}
}

type sequentialFillRetryProbeExecutor struct {
	id string

	mu           sync.Mutex
	executeCalls int
}

func (e *sequentialFillRetryProbeExecutor) Identifier() string {
	return e.id
}

func (e *sequentialFillRetryProbeExecutor) Execute(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.executeCalls++
	if e.executeCalls == 1 {
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *sequentialFillRetryProbeExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *sequentialFillRetryProbeExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *sequentialFillRetryProbeExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *sequentialFillRetryProbeExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *sequentialFillRetryProbeExecutor) ExecuteCalls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.executeCalls
}

func TestManager_MarkResult_429StaysSoftBeforeRepeatedQuotaFailures(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "soft-quota-auth",
		Provider: "claude",
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "soft-quota-model"
	result := Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
	}

	var updated *Auth
	var state *ModelState
	for attempt := 1; attempt < quotaHardCooldownFailures; attempt++ {
		m.MarkResult(context.Background(), result)
		var ok bool
		updated, ok = m.GetByID(auth.ID)
		if !ok {
			t.Fatalf("auth not found after %d 429s", attempt)
		}
		state = updated.ModelStates[model]
		if state == nil {
			t.Fatalf("model state not found after %d 429s", attempt)
		}
		if !state.NextRetryAfter.IsZero() {
			t.Fatalf("%d 429s cooldown = %v, want zero", attempt, state.NextRetryAfter)
		}
		if !state.Quota.Exceeded {
			t.Fatalf("%d 429s should mark quota pressure", attempt)
		}
		if got := state.Health.ConsecutiveFailures; got != attempt {
			t.Fatalf("%d 429s consecutive failures = %d, want %d", attempt, got, attempt)
		}
	}

	before := time.Now()
	m.MarkResult(context.Background(), result)
	after := time.Now()
	updated, ok := m.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth not found after %d 429s", quotaHardCooldownFailures)
	}
	state = updated.ModelStates[model]
	if state == nil {
		t.Fatalf("model state not found after %d 429s", quotaHardCooldownFailures)
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatalf("expected %d 429s to set hard cooldown", quotaHardCooldownFailures)
	}
	expectedCooldown := healthOpenCooldown(http.StatusTooManyRequests, quotaHardCooldownFailures)
	minExpected := before.Add(expectedCooldown - 5*time.Second)
	maxExpected := after.Add(expectedCooldown + 5*time.Second)
	if state.NextRetryAfter.Before(minExpected) || state.NextRetryAfter.After(maxExpected) {
		t.Fatalf("%d 429s cooldown = %v, want within [%v, %v]", quotaHardCooldownFailures, state.NextRetryAfter, minExpected, maxExpected)
	}
}

func TestManager_MarkResult_429ModerateRetryAfterDoesNotHardCooldownImmediately(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "moderate-retry-after-auth",
		Provider: "claude",
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "moderate-retry-after-model"
	retryAfter := 5 * time.Minute
	m.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   auth.Provider,
		Model:      model,
		Success:    false,
		RetryAfter: &retryAfter,
		Error:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok {
		t.Fatal("auth not found after 429")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatal("model state not found after 429")
	}
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("cooldown = %v, want zero for moderate retry-after before repeated failures", state.NextRetryAfter)
	}
	if !state.Quota.Exceeded {
		t.Fatal("expected quota pressure to be recorded")
	}
	if got := state.Health.ConsecutiveFailures; got != 1 {
		t.Fatalf("consecutive failures = %d, want 1", got)
	}
}

func TestManager_MarkResult_KimiBillingCycleQuotaBlocksEntireAuth(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "kimi-billing-cycle-quota-auth",
		Provider: "kimi",
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "kimi-k2.6"
	message := "You've reached your usage limit for this billing cycle. Your quota will be refreshed in the next cycle. Upgrade to get more: https://www.kimi.com/code/console?from=quota-upgrade"
	before := time.Now()
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusForbidden, Message: message},
	})
	after := time.Now()

	updated, ok := m.GetByID(auth.ID)
	if !ok {
		t.Fatal("auth not found after billing cycle quota failure")
	}
	if !updated.Unavailable {
		t.Fatal("auth should be unavailable after billing cycle quota failure")
	}
	if updated.Status != StatusError {
		t.Fatalf("status = %s, want %s", updated.Status, StatusError)
	}
	if updated.StatusMessage != "billing cycle quota exhausted" {
		t.Fatalf("status message = %q, want billing cycle quota exhausted", updated.StatusMessage)
	}
	if !updated.Quota.Exceeded || updated.Quota.Reason != "billing_cycle_quota" {
		t.Fatalf("auth quota = %+v, want billing_cycle_quota exceeded", updated.Quota)
	}
	minExpected := before.Add(accountQuotaCooldown)
	maxExpected := after.Add(accountQuotaCooldown + time.Second)
	if updated.NextRetryAfter.Before(minExpected) || updated.NextRetryAfter.After(maxExpected) {
		t.Fatalf("auth next retry = %v, want within [%v, %v]", updated.NextRetryAfter, minExpected, maxExpected)
	}

	state := updated.ModelStates[model]
	if state == nil {
		t.Fatal("model state not found after billing cycle quota failure")
	}
	if !state.Quota.Exceeded || state.Quota.Reason != "billing_cycle_quota" {
		t.Fatalf("model quota = %+v, want billing_cycle_quota exceeded", state.Quota)
	}

	blocked, reason, next := isAuthBlockedForModel(updated, "kimi-k2.6-alt", time.Now())
	if !blocked {
		t.Fatal("auth should be blocked for other models")
	}
	if reason != blockReasonCooldown {
		t.Fatalf("block reason = %v, want %v", reason, blockReasonCooldown)
	}
	if next.IsZero() {
		t.Fatal("block should include next retry time")
	}
}

func TestManager_Execute_SequentialFillMaxRetryCredentialsAllowsThreeFallbacks(t *testing.T) {
	model := "sf-max-retry-credentials-model"
	selector := &SequentialFillSelector{
		current: map[string]string{
			"claude:" + model: "b",
		},
	}
	manager := NewManager(nil, selector, nil)
	manager.SetRetryConfig(0, 0, 3)

	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"b": &Error{HTTPStatus: http.StatusInternalServerError, Message: "boom-b"},
			"c": &Error{HTTPStatus: http.StatusInternalServerError, Message: "boom-c"},
			"d": &Error{HTTPStatus: http.StatusInternalServerError, Message: "boom-d"},
		},
	}
	manager.RegisterExecutor(executor)

	auths := []*Auth{
		{ID: "a", Provider: "claude"},
		{ID: "b", Provider: "claude"},
		{ID: "c", Provider: "claude"},
		{ID: "d", Provider: "claude"},
	}

	reg := registry.GetGlobalRegistry()
	for _, auth := range auths {
		reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth %s: %v", auth.ID, errRegister)
		}
	}
	t.Cleanup(func() {
		for _, auth := range auths {
			reg.UnregisterClient(auth.ID)
		}
	})

	resp, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success after three fallback credentials", errExecute)
	}
	if string(resp.Payload) != "a" {
		t.Fatalf("payload = %q, want %q from successful auth fallback", string(resp.Payload), "a")
	}
	if got := executor.ExecuteCalls(); len(got) != 4 {
		t.Fatalf("execute calls = %v, want four attempts [b c d a]", got)
	}
	want := []string{"b", "c", "d", "a"}
	for i, authID := range want {
		if got := executor.ExecuteCalls()[i]; got != authID {
			t.Fatalf("execute call %d auth = %q, want %q", i, got, authID)
		}
	}
}

func TestManager_Execute_RetryQueueDelayDelaysCredentialFallback(t *testing.T) {
	model := "retry-queue-delay-model"
	selector := &SequentialFillSelector{
		current: map[string]string{
			"claude:" + model: "bad",
		},
	}
	manager := NewManager(nil, selector, nil)
	manager.SetRetryConfig(0, 0, 0)
	queueDelay := 20 * time.Millisecond
	manager.SetRetryQueueDelay(queueDelay)

	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"bad": &Error{HTTPStatus: http.StatusInternalServerError, Message: "boom"},
		},
	}
	manager.RegisterExecutor(executor)

	auths := []*Auth{
		{ID: "bad", Provider: "claude"},
		{ID: "good", Provider: "claude"},
	}

	reg := registry.GetGlobalRegistry()
	for _, auth := range auths {
		reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth %s: %v", auth.ID, errRegister)
		}
	}
	t.Cleanup(func() {
		for _, auth := range auths {
			reg.UnregisterClient(auth.ID)
		}
	})

	start := time.Now()
	resp, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	elapsed := time.Since(start)
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success after delayed fallback", errExecute)
	}
	if string(resp.Payload) != "good" {
		t.Fatalf("payload = %q, want %q from successful auth fallback", string(resp.Payload), "good")
	}
	if elapsed < queueDelay {
		t.Fatalf("elapsed = %v, want at least retry queue delay %v", elapsed, queueDelay)
	}
	calls := executor.ExecuteCalls()
	if len(calls) != 2 || calls[0] != "bad" || calls[1] != "good" {
		t.Fatalf("execute calls = %v, want [bad good]", calls)
	}
}

func TestManager_Execute_OpenAICompatChannelBreakerBypassesHigherPriorityChannel(t *testing.T) {
	model := "gpt-5-channel-breaker"
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 1)

	failingExecutor := &authFallbackExecutor{
		id: "xixiapi-plus",
		executeErrors: map[string]error{
			"xixi-1": &Error{HTTPStatus: http.StatusTooManyRequests, Message: "no available channel"},
			"xixi-2": &Error{HTTPStatus: http.StatusTooManyRequests, Message: "no available channel"},
			"xixi-3": &Error{HTTPStatus: http.StatusTooManyRequests, Message: "no available channel"},
		},
	}
	backupExecutor := &authFallbackExecutor{id: "backup-gpt"}
	manager.RegisterExecutor(failingExecutor)
	manager.RegisterExecutor(backupExecutor)

	auths := []*Auth{
		openAICompatChannelBreakerAuth("xixi-1", "xixiapi-plus", "https://xixiapi.cc/v1", 10),
		openAICompatChannelBreakerAuth("xixi-2", "xixiapi-plus", "https://xixiapi.cc/v1", 10),
		openAICompatChannelBreakerAuth("xixi-3", "xixiapi-plus", "https://xixiapi.cc/v1", 10),
		openAICompatChannelBreakerAuth("backup-1", "backup-gpt", "https://backup.example.com/v1", 1),
	}

	reg := registry.GetGlobalRegistry()
	for _, auth := range auths {
		reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth %s: %v", auth.ID, errRegister)
		}
	}
	t.Cleanup(func() {
		for _, auth := range auths {
			reg.UnregisterClient(auth.ID)
		}
	})

	_, errFirst := manager.Execute(context.Background(), []string{"xixiapi-plus", "backup-gpt"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errFirst == nil {
		t.Fatal("first execute unexpectedly succeeded before the channel breaker opened")
	}
	if got := failingExecutor.ExecuteCalls(); len(got) != 2 {
		t.Fatalf("first execute calls = %v, want two high-priority attempts", got)
	}

	resp, errSecond := manager.Execute(context.Background(), []string{"xixiapi-plus", "backup-gpt"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errSecond != nil {
		t.Fatalf("second execute error = %v, want fallback channel success", errSecond)
	}
	if string(resp.Payload) != "backup-1" {
		t.Fatalf("payload = %q, want backup-1", string(resp.Payload))
	}
	if got := failingExecutor.ExecuteCalls(); len(got) != channelBreakerOpenFailures {
		t.Fatalf("failing channel calls = %v, want %d before breaker bypass", got, channelBreakerOpenFailures)
	}
	if got := backupExecutor.ExecuteCalls(); len(got) != 1 || got[0] != "backup-1" {
		t.Fatalf("backup calls = %v, want [backup-1]", got)
	}
	for _, authID := range []string{"xixi-1", "xixi-2", "xixi-3"} {
		updated, ok := manager.GetByID(authID)
		if !ok {
			t.Fatalf("auth %s not found", authID)
		}
		blocked, reason, next := isAuthBlockedForModel(updated, model, time.Now())
		if !blocked || reason != blockReasonCooldown || next.IsZero() {
			t.Fatalf("auth %s blocked=%v reason=%v next=%v, want channel cooldown", authID, blocked, reason, next)
		}
	}
}

func TestManager_MarkResult_OpenAICompatChannelBreakerScopesToRequestedAlias(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auths := []*Auth{
		openAICompatChannelBreakerAuth("minimax-1", "minimax", "https://api.minimax.io/v1", 10),
		openAICompatChannelBreakerAuth("minimax-2", "minimax", "https://api.minimax.io/v1", 10),
	}
	routeModel := "claude-sonnet-4-6"
	otherAlias := "other-claude-alias"
	upstreamModel := "MiniMax-M2.5"

	reg := registry.GetGlobalRegistry()
	for _, auth := range auths {
		reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{
			{ID: routeModel},
			{ID: otherAlias},
		})
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth %s: %v", auth.ID, errRegister)
		}
	}
	t.Cleanup(func() {
		for _, auth := range auths {
			reg.UnregisterClient(auth.ID)
		}
	})

	ctx := coreusage.WithRequestedModelAlias(context.Background(), routeModel)
	for i := 0; i < channelBreakerOpenFailures; i++ {
		manager.MarkResult(ctx, Result{
			AuthID:   auths[0].ID,
			Provider: auths[0].Provider,
			Model:    upstreamModel,
			Success:  false,
			Error: &Error{
				HTTPStatus: http.StatusTooManyRequests,
				Message:    "no available channel",
			},
		})
	}

	currentAuths := make([]*Auth, 0, len(auths))
	for _, auth := range auths {
		updated, ok := manager.GetByID(auth.ID)
		if !ok || updated == nil {
			t.Fatalf("auth %s not found", auth.ID)
		}
		currentAuths = append(currentAuths, updated)
	}

	availableRoute, errRoute := manager.availableAuthsForRouteModel(currentAuths, "mixed", routeModel, time.Now())
	if errRoute != nil {
		var cooldownErr *modelCooldownError
		if !errors.As(errRoute, &cooldownErr) {
			t.Fatalf("route model error = %T, want nil or *modelCooldownError", errRoute)
		}
	}
	if len(availableRoute) > 1 {
		t.Fatalf("availableAuthsForRouteModel(routeModel) len = %d, want at most one half-open probe candidate", len(availableRoute))
	}

	availableOther, errOther := manager.availableAuthsForRouteModel(currentAuths, "mixed", otherAlias, time.Now())
	if errOther != nil {
		t.Fatalf("availableAuthsForRouteModel(otherAlias) error = %v", errOther)
	}
	if len(availableOther) != len(currentAuths) {
		t.Fatalf("availableAuthsForRouteModel(otherAlias) len = %d, want %d", len(availableOther), len(currentAuths))
	}

	for _, updated := range currentAuths {
		if state := updated.ModelStates[upstreamModel]; state != nil {
			t.Fatalf("auth %s upstream state = %#v, want breaker to avoid upstream-model contamination", updated.ID, state)
		}
		state := updated.ModelStates[routeModel]
		if state == nil || !state.Unavailable || state.NextRetryAfter.IsZero() {
			t.Fatalf("auth %s route alias state = %#v, want unavailable alias-scoped cooldown", updated.ID, state)
		}
	}
}

func TestManager_Execute_TransientNetworkTriesAllCredentialsWithoutCooldown(t *testing.T) {
	model := "transient-network-model"
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 1)

	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"aa-reset-auth": &Error{
				HTTPStatus: http.StatusInternalServerError,
				Message:    "read tcp 10.0.0.1:52886->10.0.0.2:443: read: connection reset by peer",
			},
			"bb-eof-auth": &Error{
				HTTPStatus: http.StatusInternalServerError,
				Message:    "unexpected EOF",
			},
		},
	}
	manager.RegisterExecutor(executor)

	auths := []*Auth{
		{ID: "aa-reset-auth", Provider: "claude"},
		{ID: "bb-eof-auth", Provider: "claude"},
		{ID: "cc-good-auth", Provider: "claude"},
	}

	reg := registry.GetGlobalRegistry()
	for _, auth := range auths {
		reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth %s: %v", auth.ID, errRegister)
		}
	}
	t.Cleanup(func() {
		for _, auth := range auths {
			reg.UnregisterClient(auth.ID)
		}
	})

	resp, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success after transient network fallback", errExecute)
	}
	if string(resp.Payload) != "cc-good-auth" {
		t.Fatalf("payload = %q, want cc-good-auth", string(resp.Payload))
	}
	got := executor.ExecuteCalls()
	want := []string{"aa-reset-auth", "bb-eof-auth", "cc-good-auth"}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d auth = %q, want %q", i, got[i], want[i])
		}
	}
	for _, authID := range []string{"aa-reset-auth", "bb-eof-auth"} {
		updated, ok := manager.GetByID(authID)
		if !ok || updated == nil {
			t.Fatalf("auth %s not found", authID)
		}
		if updated.Unavailable {
			t.Fatalf("auth %s should remain available after transient network fallback", authID)
		}
		if state := updated.ModelStates[model]; state != nil {
			t.Fatalf("auth %s should not have model cooldown after transient network fallback: %#v", authID, state)
		}
	}
}

func TestManager_Execute_MiniMaxUnknown1000TriesCredentialsWithoutCooldown(t *testing.T) {
	model := "MiniMax-M2.7-highspeed"
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 10)

	executor := &authFallbackExecutor{
		id: "minimax",
		executeErrors: map[string]error{
			"aa-unknown-auth": &Error{
				Code:       "server_error",
				HTTPStatus: http.StatusInternalServerError,
				Message:    miniMaxUnknown1000Message,
			},
			"bb-unknown-auth": &Error{
				Code:       "1000",
				HTTPStatus: http.StatusInternalServerError,
				Message:    "unknown error",
			},
		},
	}
	manager.RegisterExecutor(executor)

	auths := []*Auth{
		{ID: "aa-unknown-auth", Provider: "minimax"},
		{ID: "bb-unknown-auth", Provider: "minimax"},
		{ID: "cc-good-auth", Provider: "minimax"},
	}

	reg := registry.GetGlobalRegistry()
	for _, auth := range auths {
		reg.RegisterClient(auth.ID, "minimax", []*registry.ModelInfo{{ID: model}})
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth %s: %v", auth.ID, errRegister)
		}
	}
	t.Cleanup(func() {
		for _, auth := range auths {
			reg.UnregisterClient(auth.ID)
		}
	})

	resp, errExecute := manager.Execute(context.Background(), []string{"minimax"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success after MiniMax 1000 fallback", errExecute)
	}
	if string(resp.Payload) != "cc-good-auth" {
		t.Fatalf("payload = %q, want cc-good-auth", string(resp.Payload))
	}
	got := executor.ExecuteCalls()
	want := []string{"aa-unknown-auth", "bb-unknown-auth", "cc-good-auth"}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d auth = %q, want %q", i, got[i], want[i])
		}
	}
	for _, authID := range []string{"aa-unknown-auth", "bb-unknown-auth"} {
		updated, ok := manager.GetByID(authID)
		if !ok || updated == nil {
			t.Fatalf("auth %s not found", authID)
		}
		if updated.Unavailable {
			t.Fatalf("auth %s should remain available after MiniMax 1000 fallback", authID)
		}
		if state := updated.ModelStates[model]; state != nil {
			t.Fatalf("auth %s should not have model cooldown after MiniMax 1000 fallback: %#v", authID, state)
		}
	}
}

func TestManager_Execute_MiniMaxNewSensitiveStopsRetryWithoutCooldown(t *testing.T) {
	model := "MiniMax-M3"
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 10)

	executor := &authFallbackExecutor{
		id: "minimax",
		executeErrors: map[string]error{
			"aa-sensitive-auth": &Error{
				Code:       "1026",
				HTTPStatus: http.StatusInternalServerError,
				Message:    miniMaxNewSensitiveMessage,
			},
		},
	}
	manager.RegisterExecutor(executor)

	auths := []*Auth{
		{ID: "aa-sensitive-auth", Provider: "minimax"},
		{ID: "bb-good-auth", Provider: "minimax"},
	}

	reg := registry.GetGlobalRegistry()
	for _, auth := range auths {
		reg.RegisterClient(auth.ID, "minimax", []*registry.ModelInfo{{ID: model}})
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth %s: %v", auth.ID, errRegister)
		}
	}
	t.Cleanup(func() {
		for _, auth := range auths {
			reg.UnregisterClient(auth.ID)
		}
	})

	_, errExecute := manager.Execute(context.Background(), []string{"minimax"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected MiniMax new_sensitive error")
	}
	if !isRequestInvalidError(errExecute) {
		t.Fatalf("expected request invalid error, got %v", errExecute)
	}
	got := executor.ExecuteCalls()
	want := []string{"aa-sensitive-auth"}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d auth = %q, want %q", i, got[i], want[i])
		}
	}
	updated, ok := manager.GetByID("aa-sensitive-auth")
	if !ok || updated == nil {
		t.Fatalf("sensitive auth not found")
	}
	if updated.Unavailable {
		t.Fatal("sensitive auth should remain available after request-scoped content safety")
	}
	if state := updated.ModelStates[model]; state != nil {
		t.Fatalf("sensitive auth should not have model cooldown: %#v", state)
	}
}

func TestManager_MarkResult_MiniMaxRequestScopedErrorsDoNotOpenChannelBreaker(t *testing.T) {
	tests := []struct {
		name string
		err  *Error
	}{
		{
			name: "new sensitive",
			err: &Error{
				Code:       "1026",
				HTTPStatus: http.StatusInternalServerError,
				Message:    miniMaxNewSensitiveMessage,
			},
		},
		{
			name: "unknown 1000",
			err: &Error{
				Code:       "server_error",
				HTTPStatus: http.StatusInternalServerError,
				Message:    miniMaxUnknown1000Message,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := "MiniMax-M3"
			manager := NewManager(nil, nil, nil)
			auths := []*Auth{
				openAICompatChannelBreakerAuth("minimax-1", "minimax", "https://api.minimax.io/v1", 10),
				openAICompatChannelBreakerAuth("minimax-2", "minimax", "https://api.minimax.io/v1", 10),
				openAICompatChannelBreakerAuth("minimax-3", "minimax", "https://api.minimax.io/v1", 10),
			}

			reg := registry.GetGlobalRegistry()
			for _, auth := range auths {
				reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
				if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
					t.Fatalf("register auth %s: %v", auth.ID, errRegister)
				}
			}
			t.Cleanup(func() {
				for _, auth := range auths {
					reg.UnregisterClient(auth.ID)
				}
			})

			for i := 0; i < channelBreakerOpenFailures; i++ {
				manager.MarkResult(context.Background(), Result{
					AuthID:   auths[0].ID,
					Provider: auths[0].Provider,
					Model:    model,
					Success:  false,
					Error:    tt.err,
				})
			}

			for _, auth := range auths {
				updated, ok := manager.GetByID(auth.ID)
				if !ok || updated == nil {
					t.Fatalf("auth %s not found", auth.ID)
				}
				if updated.Unavailable {
					t.Fatalf("auth %s should remain available", auth.ID)
				}
				blocked, reason, next := isAuthBlockedForModel(updated, model, time.Now())
				if blocked || reason != blockReasonNone || !next.IsZero() {
					t.Fatalf("auth %s blocked=%v reason=%v next=%v, want no channel breaker cooldown", auth.ID, blocked, reason, next)
				}
				if state := updated.ModelStates[model]; state != nil {
					t.Fatalf("auth %s should not have model state after %s: %#v", auth.ID, tt.name, state)
				}
			}
		})
	}
}

func openAICompatChannelBreakerAuth(id, provider, baseURL string, priority int) *Auth {
	return &Auth{
		ID:       id,
		Provider: provider,
		Prefix:   provider,
		Attributes: map[string]string{
			"base_url":        baseURL,
			"compat_name":     provider,
			"provider_family": "openai-compatibility",
			"provider_key":    provider,
			"priority":        strconv.Itoa(priority),
		},
	}
}

type credentialRetryLimitExecutor struct {
	id  string
	err error

	mu    sync.Mutex
	calls int
}

func (e *credentialRetryLimitExecutor) Identifier() string {
	return e.id
}

func (e *credentialRetryLimitExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.recordCall()
	return cliproxyexecutor.Response{}, e.executionError()
}

func (e *credentialRetryLimitExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.recordCall()
	return nil, e.executionError()
}

func (e *credentialRetryLimitExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *credentialRetryLimitExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.recordCall()
	return cliproxyexecutor.Response{}, e.executionError()
}

func (e *credentialRetryLimitExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *credentialRetryLimitExecutor) recordCall() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
}

func (e *credentialRetryLimitExecutor) executionError() error {
	if e.err != nil {
		return e.err
	}
	return &Error{HTTPStatus: 500, Message: "boom"}
}

func (e *credentialRetryLimitExecutor) Calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

type authFallbackExecutor struct {
	id string

	mu                sync.Mutex
	executeCalls      []string
	countCalls        []string
	streamCalls       []string
	executeErrors     map[string]error
	countErrors       map[string]error
	streamFirstErrors map[string]error
}

func (e *authFallbackExecutor) Identifier() string {
	return e.id
}

func (e *authFallbackExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.executeCalls = append(e.executeCalls, auth.ID)
	err := e.executeErrors[auth.ID]
	e.mu.Unlock()
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *authFallbackExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.streamCalls = append(e.streamCalls, auth.ID)
	err := e.streamFirstErrors[auth.ID]
	e.mu.Unlock()

	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	if err != nil {
		ch <- cliproxyexecutor.StreamChunk{Err: err}
		close(ch)
		return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Auth": {auth.ID}}, Chunks: ch}, nil
	}
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Auth": {auth.ID}}, Chunks: ch}, nil
}

func (e *authFallbackExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *authFallbackExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.countCalls = append(e.countCalls, auth.ID)
	err := e.countErrors[auth.ID]
	e.mu.Unlock()
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *authFallbackExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *authFallbackExecutor) ExecuteCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.executeCalls))
	copy(out, e.executeCalls)
	return out
}

func (e *authFallbackExecutor) StreamCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.streamCalls))
	copy(out, e.streamCalls)
	return out
}

func (e *authFallbackExecutor) CountCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.countCalls))
	copy(out, e.countCalls)
	return out
}

type deleteTrackingStore struct {
	mu         sync.Mutex
	deletedIDs []string
}

func (s *deleteTrackingStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *deleteTrackingStore) Save(context.Context, *Auth) (string, error) { return "", nil }

func (s *deleteTrackingStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletedIDs = append(s.deletedIDs, id)
	return nil
}

func (s *deleteTrackingStore) DeletedIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.deletedIDs))
	copy(out, s.deletedIDs)
	return out
}

func newUnauthorizedEvictionTestManager(t *testing.T) (*Manager, *authFallbackExecutor, *deleteTrackingStore, string, string, string) {
	t.Helper()

	const model = "test-model"
	const badAuthID = "aa-bad-auth"
	const goodAuthID = "bb-good-auth"

	prev := deleteUnauthorizedAuthEnabled.Load()
	SetDeleteUnauthorizedAuth(true)
	t.Cleanup(func() { SetDeleteUnauthorizedAuth(prev) })

	store := &deleteTrackingStore{}
	selector := &SequentialFillSelector{
		current: map[string]string{
			"claude:" + model: badAuthID,
		},
	}
	manager := NewManager(store, selector, nil)
	manager.SetRetryConfig(0, 0, 1)

	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			badAuthID: &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
		},
		countErrors: map[string]error{
			badAuthID: &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
		},
		streamFirstErrors: map[string]error{
			badAuthID: &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
		},
	}
	manager.RegisterExecutor(executor)

	badAuth := &Auth{ID: badAuthID, Provider: "claude", Metadata: map[string]any{"type": "claude"}}
	goodAuth := &Auth{ID: goodAuthID, Provider: "claude", Metadata: map[string]any{"type": "claude"}}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(badAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(goodAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(badAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	if _, errRegister := manager.Register(context.Background(), badAuth); errRegister != nil {
		t.Fatalf("register bad auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), goodAuth); errRegister != nil {
		t.Fatalf("register good auth: %v", errRegister)
	}

	return manager, executor, store, model, badAuthID, goodAuthID
}

func assertUnauthorizedAuthEvicted(t *testing.T, manager *Manager, store *deleteTrackingStore, badAuthID string) {
	t.Helper()
	if _, ok := manager.GetByID(badAuthID); ok {
		t.Fatalf("expected unauthorized auth %q to be evicted", badAuthID)
	}
	gotDeleted := store.DeletedIDs()
	if len(gotDeleted) != 1 || gotDeleted[0] != badAuthID {
		t.Fatalf("deleted auth IDs = %v, want [%s]", gotDeleted, badAuthID)
	}
}

type retryAfterStatusError struct {
	status     int
	message    string
	retryAfter time.Duration
}

func (e *retryAfterStatusError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func (e *retryAfterStatusError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.status
}

func (e *retryAfterStatusError) RetryAfter() *time.Duration {
	if e == nil {
		return nil
	}
	d := e.retryAfter
	return &d
}

type codedStatusError struct {
	status int
	code   string
	msg    string
}

func (e codedStatusError) Error() string {
	return e.msg
}

func (e codedStatusError) StatusCode() int {
	return e.status
}

func (e codedStatusError) ErrorCode() string {
	return e.code
}

type emptyStreamRetryExecutor struct {
	id string

	mu    sync.Mutex
	calls int
}

func (e *emptyStreamRetryExecutor) Identifier() string {
	return e.id
}

func (e *emptyStreamRetryExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "Execute not implemented"}
}

func (e *emptyStreamRetryExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.calls++
	calls := e.calls
	e.mu.Unlock()

	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	if calls == 1 {
		close(ch)
		return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
	}
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("ok")}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *emptyStreamRetryExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *emptyStreamRetryExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *emptyStreamRetryExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *emptyStreamRetryExecutor) Calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func TestManagerExecuteStream_RetriesRetryableEmptyStream(t *testing.T) {
	const model = "gpt-5.5"
	const authID = "empty-stream-auth"

	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(1, 30*time.Second, 0)

	executor := &emptyStreamRetryExecutor{id: "codex"}
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	if _, errRegister := m.Register(context.Background(), &Auth{ID: authID, Provider: "codex"}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	streamResult, errExecute := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute stream: %v", errExecute)
	}

	var payload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if string(payload) != "ok" {
		t.Fatalf("payload = %q, want ok", string(payload))
	}
	if got := executor.Calls(); got != 2 {
		t.Fatalf("stream calls = %d, want 2", got)
	}
}

func newCredentialRetryLimitTestManager(t *testing.T, maxRetryCredentials int) (*Manager, *credentialRetryLimitExecutor) {
	t.Helper()

	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(0, 0, maxRetryCredentials)

	executor := &credentialRetryLimitExecutor{id: "claude"}
	m.RegisterExecutor(executor)

	baseID := uuid.NewString()
	auth1 := &Auth{ID: baseID + "-auth-1", Provider: "claude"}
	auth2 := &Auth{ID: baseID + "-auth-2", Provider: "claude"}

	// Auth selection requires that the global model registry knows each credential supports the model.
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "claude", []*registry.ModelInfo{{ID: "test-model"}})
	reg.RegisterClient(auth2.ID, "claude", []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	if _, errRegister := m.Register(context.Background(), auth1); errRegister != nil {
		t.Fatalf("register auth1: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), auth2); errRegister != nil {
		t.Fatalf("register auth2: %v", errRegister)
	}

	return m, executor
}

func TestManager_MaxRetryCredentials_LimitsCrossCredentialRetries(t *testing.T) {
	request := cliproxyexecutor.Request{Model: "test-model"}
	testCases := []struct {
		name   string
		invoke func(*Manager) error
	}{
		{
			name: "execute",
			invoke: func(m *Manager) error {
				_, errExecute := m.Execute(context.Background(), []string{"claude"}, request, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "execute_count",
			invoke: func(m *Manager) error {
				_, errExecute := m.ExecuteCount(context.Background(), []string{"claude"}, request, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "execute_stream",
			invoke: func(m *Manager) error {
				_, errExecute := m.ExecuteStream(context.Background(), []string{"claude"}, request, cliproxyexecutor.Options{})
				return errExecute
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			limitedManager, limitedExecutor := newCredentialRetryLimitTestManager(t, 1)
			if errInvoke := tc.invoke(limitedManager); errInvoke == nil {
				t.Fatalf("expected error for limited retry execution")
			}
			if calls := limitedExecutor.Calls(); calls != 2 {
				t.Fatalf("expected 2 calls with max-retry-credentials=1, got %d", calls)
			}

			unlimitedManager, unlimitedExecutor := newCredentialRetryLimitTestManager(t, 0)
			if errInvoke := tc.invoke(unlimitedManager); errInvoke == nil {
				t.Fatalf("expected error for unlimited retry execution")
			}
			if calls := unlimitedExecutor.Calls(); calls != 2 {
				t.Fatalf("expected 2 calls with max-retry-credentials=0, got %d", calls)
			}
		})
	}
}

func TestManager_MaxRetryCredentials_LimitsTransientRoutingFallback(t *testing.T) {
	model := "MiniMax-M2.7-highspeed"
	request := cliproxyexecutor.Request{Model: model}
	transientErr := &Error{
		Code:       "server_error",
		HTTPStatus: http.StatusInternalServerError,
		Message:    miniMaxUnknown1000Message,
	}
	testCases := []struct {
		name   string
		invoke func(*Manager) error
	}{
		{
			name: "execute",
			invoke: func(m *Manager) error {
				_, errExecute := m.executeMixedOnce(context.Background(), []string{"minimax"}, request, cliproxyexecutor.Options{}, 1)
				return errExecute
			},
		},
		{
			name: "execute_count",
			invoke: func(m *Manager) error {
				_, errExecute := m.executeCountMixedOnce(context.Background(), []string{"minimax"}, request, cliproxyexecutor.Options{}, 1)
				return errExecute
			},
		},
		{
			name: "execute_stream",
			invoke: func(m *Manager) error {
				_, errExecute := m.executeStreamMixedOnce(context.Background(), []string{"minimax"}, request, cliproxyexecutor.Options{}, 1)
				return errExecute
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			executor := &credentialRetryLimitExecutor{id: "minimax", err: transientErr}
			manager.RegisterExecutor(executor)

			auths := []*Auth{
				{ID: "transient-auth-1", Provider: "minimax"},
				{ID: "transient-auth-2", Provider: "minimax"},
				{ID: "transient-auth-3", Provider: "minimax"},
				{ID: "transient-auth-4", Provider: "minimax"},
			}
			reg := registry.GetGlobalRegistry()
			for _, auth := range auths {
				reg.RegisterClient(auth.ID, "minimax", []*registry.ModelInfo{{ID: model}})
				if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
					t.Fatalf("register auth %s: %v", auth.ID, errRegister)
				}
			}
			t.Cleanup(func() {
				for _, auth := range auths {
					reg.UnregisterClient(auth.ID)
				}
			})

			if errInvoke := tc.invoke(manager); errInvoke == nil {
				t.Fatalf("expected transient routing error for limited retry execution")
			}
			if calls := executor.Calls(); calls != 2 {
				t.Fatalf("expected 2 calls with max-retry-credentials=1, got %d", calls)
			}
		})
	}
}

func TestManager_GPTLargeToolResponses_LimitsCodexCredentialFallback(t *testing.T) {
	const model = "gpt-5.5"
	testCases := []struct {
		name      string
		invoke    func(*Manager, cliproxyexecutor.Request, cliproxyexecutor.Options) error
		getCalls  func(*authFallbackExecutor) []string
		errorMap  func(error) *authFallbackExecutor
		wantCalls []string
	}{
		{
			name: "execute",
			invoke: func(m *Manager, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
				_, errExecute := m.executeMixedOnce(context.Background(), []string{"codex"}, req, opts, 0)
				return errExecute
			},
			getCalls: func(e *authFallbackExecutor) []string { return e.ExecuteCalls() },
			errorMap: func(err error) *authFallbackExecutor {
				return &authFallbackExecutor{
					id: "codex",
					executeErrors: map[string]error{
						"aa-codex-1": err,
						"ab-codex-2": err,
						"ba-codex-3": err,
					},
				}
			},
			wantCalls: []string{"aa-codex-1", "ba-codex-3"},
		},
		{
			name: "execute_count",
			invoke: func(m *Manager, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
				_, errExecute := m.executeCountMixedOnce(context.Background(), []string{"codex"}, req, opts, 0)
				return errExecute
			},
			getCalls: func(e *authFallbackExecutor) []string { return e.CountCalls() },
			errorMap: func(err error) *authFallbackExecutor {
				return &authFallbackExecutor{
					id: "codex",
					countErrors: map[string]error{
						"aa-codex-1": err,
						"ab-codex-2": err,
						"ba-codex-3": err,
					},
				}
			},
			wantCalls: []string{"aa-codex-1", "ba-codex-3"},
		},
		{
			name: "execute_stream",
			invoke: func(m *Manager, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
				_, errExecute := m.executeStreamMixedOnce(context.Background(), []string{"codex"}, req, opts, 0)
				return errExecute
			},
			getCalls: func(e *authFallbackExecutor) []string { return e.StreamCalls() },
			errorMap: func(err error) *authFallbackExecutor {
				return &authFallbackExecutor{
					id: "codex",
					streamFirstErrors: map[string]error{
						"aa-codex-1": err,
						"ab-codex-2": err,
						"ba-codex-3": err,
					},
				}
			},
			wantCalls: []string{"aa-codex-1", "ba-codex-3"},
		},
	}

	errUpstream := &Error{HTTPStatus: http.StatusInternalServerError, Message: "api_error"}
	request := cliproxyexecutor.Request{Model: model}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.RequestPathMetadataKey:     "/v1/responses",
		cliproxyexecutor.MessageCountMetadataKey:    187,
		cliproxyexecutor.ToolCountMetadataKey:       60,
		cliproxyexecutor.ReasoningEffortMetadataKey: "high",
	}}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			manager, executor := newGPTLargeToolResponsesFallbackManager(t, model, tc.errorMap(errUpstream))

			if errInvoke := tc.invoke(manager, request, opts); errInvoke == nil {
				t.Fatalf("expected guarded large tool request to fail after capped fallback")
			}
			if got := tc.getCalls(executor); !stringSlicesEqual(got, tc.wantCalls) {
				t.Fatalf("calls = %v, want %v", got, tc.wantCalls)
			}
		})
	}
}

func TestManager_GPTSmallResponses_KeepsConfiguredCodexFallback(t *testing.T) {
	const model = "gpt-5.5"
	errUpstream := &Error{HTTPStatus: http.StatusInternalServerError, Message: "api_error"}
	executor := &authFallbackExecutor{
		id: "codex",
		executeErrors: map[string]error{
			"aa-codex-1": errUpstream,
			"ab-codex-2": errUpstream,
			"ba-codex-3": errUpstream,
		},
	}
	manager, executor := newGPTLargeToolResponsesFallbackManager(t, model, executor)

	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.RequestPathMetadataKey:  "/v1/responses",
		cliproxyexecutor.MessageCountMetadataKey: 20,
		cliproxyexecutor.ToolCountMetadataKey:    8,
	}}
	resp, errExecute := manager.executeMixedOnce(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, opts, 0)
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success through normal fallback", errExecute)
	}
	if string(resp.Payload) != "ca-codex-4" {
		t.Fatalf("payload = %q, want ca-codex-4", string(resp.Payload))
	}
	want := []string{"aa-codex-1", "ab-codex-2", "ba-codex-3", "ca-codex-4"}
	if got := executor.ExecuteCalls(); !stringSlicesEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

func newGPTLargeToolResponsesFallbackManager(t *testing.T, model string, executor *authFallbackExecutor) (*Manager, *authFallbackExecutor) {
	t.Helper()

	selector := &SequentialFillSelector{
		current: map[string]string{
			"codex:" + model: "aa-codex-1",
		},
	}
	manager := NewManager(nil, selector, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(executor)

	auths := []*Auth{
		{ID: "aa-codex-1", Provider: "codex", Attributes: map[string]string{"routing_group": "group-a"}},
		{ID: "ab-codex-2", Provider: "codex", Attributes: map[string]string{"routing_group": "group-a"}},
		{ID: "ba-codex-3", Provider: "codex", Attributes: map[string]string{"routing_group": "group-b"}},
		{ID: "ca-codex-4", Provider: "codex", Attributes: map[string]string{"routing_group": "group-c"}},
	}

	reg := registry.GetGlobalRegistry()
	for _, auth := range auths {
		reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth %s: %v", auth.ID, errRegister)
		}
	}
	t.Cleanup(func() {
		for _, auth := range auths {
			reg.UnregisterClient(auth.ID)
		}
	})

	return manager, executor
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestManager_Execute_UnauthorizedAuthEviction(t *testing.T) {
	manager, executor, store, model, badAuthID, goodAuthID := newUnauthorizedEvictionTestManager(t)

	var buf bytes.Buffer
	logger := log.StandardLogger()
	oldOut := logger.Out
	oldFormatter := logger.Formatter
	oldLevel := logger.Level
	log.SetOutput(&buf)
	log.SetFormatter(&log.TextFormatter{DisableTimestamp: true, DisableColors: true})
	log.SetLevel(log.InfoLevel)
	defer func() {
		log.SetOutput(oldOut)
		log.SetFormatter(oldFormatter)
		log.SetLevel(oldLevel)
	}()

	resp, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success", errExecute)
	}
	if string(resp.Payload) != goodAuthID {
		t.Fatalf("execute payload = %q, want %q", string(resp.Payload), goodAuthID)
	}
	if gotCalls := executor.ExecuteCalls(); len(gotCalls) != 2 || gotCalls[0] != badAuthID || gotCalls[1] != goodAuthID {
		t.Fatalf("execute calls = %v, want [%s %s]", gotCalls, badAuthID, goodAuthID)
	}
	assertUnauthorizedAuthEvicted(t, manager, store, badAuthID)
	logOutput := buf.String()
	if !strings.Contains(logOutput, "evicting unauthorized auth") {
		t.Fatalf("expected info log for unauthorized auth eviction, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, badAuthID) {
		t.Fatalf("expected log to contain auth id %q, got: %s", badAuthID, logOutput)
	}
	if !strings.Contains(logOutput, model) {
		t.Fatalf("expected log to contain model %q, got: %s", model, logOutput)
	}
}

func TestManager_ExecuteCount_UnauthorizedAuthEviction(t *testing.T) {
	manager, executor, store, model, badAuthID, goodAuthID := newUnauthorizedEvictionTestManager(t)

	resp, errExecute := manager.ExecuteCount(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute count error = %v, want success", errExecute)
	}
	if string(resp.Payload) != goodAuthID {
		t.Fatalf("execute count payload = %q, want %q", string(resp.Payload), goodAuthID)
	}
	if gotCalls := executor.CountCalls(); len(gotCalls) != 2 || gotCalls[0] != badAuthID || gotCalls[1] != goodAuthID {
		t.Fatalf("count calls = %v, want [%s %s]", gotCalls, badAuthID, goodAuthID)
	}
	assertUnauthorizedAuthEvicted(t, manager, store, badAuthID)
}

func TestManager_ExecuteStream_UnauthorizedAuthEviction(t *testing.T) {
	manager, executor, store, model, badAuthID, goodAuthID := newUnauthorizedEvictionTestManager(t)

	streamResult, errExecute := manager.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute stream error = %v, want success", errExecute)
	}
	var payload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("execute stream chunk error = %v, want success", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if string(payload) != goodAuthID {
		t.Fatalf("execute stream payload = %q, want %q", string(payload), goodAuthID)
	}
	if gotCalls := executor.StreamCalls(); len(gotCalls) != 2 || gotCalls[0] != badAuthID || gotCalls[1] != goodAuthID {
		t.Fatalf("stream calls = %v, want [%s %s]", gotCalls, badAuthID, goodAuthID)
	}
	assertUnauthorizedAuthEvicted(t, manager, store, badAuthID)
}

func TestManager_ExecuteStream_PinnedUnauthorizedBootstrapReturnsStreamError(t *testing.T) {
	manager, executor, store, model, badAuthID, _ := newUnauthorizedEvictionTestManager(t)

	streamResult, errExecute := manager.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.PinnedAuthMetadataKey: badAuthID,
		},
	})
	if errExecute != nil {
		t.Fatalf("execute stream error = %v, want nil stream setup error", errExecute)
	}
	if streamResult == nil || streamResult.Chunks == nil {
		t.Fatalf("expected non-nil stream result and chunks")
	}

	var payload []byte
	var gotErr error
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			gotErr = chunk.Err
			continue
		}
		payload = append(payload, chunk.Payload...)
	}

	if len(payload) != 0 {
		t.Fatalf("execute stream payload = %q, want empty payload", string(payload))
	}
	if gotErr == nil {
		t.Fatalf("expected terminal stream error, got nil")
	}
	if statusCodeFromError(gotErr) != http.StatusUnauthorized {
		t.Fatalf("stream error status = %d, want %d", statusCodeFromError(gotErr), http.StatusUnauthorized)
	}
	if gotCalls := executor.StreamCalls(); len(gotCalls) != 1 || gotCalls[0] != badAuthID {
		t.Fatalf("stream calls = %v, want [%s]", gotCalls, badAuthID)
	}
	assertUnauthorizedAuthEvicted(t, manager, store, badAuthID)
}

// When delete-unauthorized-auth is disabled (the default), a 401 must still
// route to the next credential but must NOT evict the bad auth from memory or
// delete it from the store. Cooldown via MarkResult is unaffected.
func TestManager_Execute_UnauthorizedAuth_DeleteDisabled_KeepsAuth(t *testing.T) {
	manager, executor, store, model, badAuthID, goodAuthID := newUnauthorizedEvictionTestManager(t)
	SetDeleteUnauthorizedAuth(false)

	resp, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success", errExecute)
	}
	if string(resp.Payload) != goodAuthID {
		t.Fatalf("execute payload = %q, want %q", string(resp.Payload), goodAuthID)
	}
	if gotCalls := executor.ExecuteCalls(); len(gotCalls) != 2 || gotCalls[0] != badAuthID || gotCalls[1] != goodAuthID {
		t.Fatalf("execute calls = %v, want [%s %s]", gotCalls, badAuthID, goodAuthID)
	}
	if _, ok := manager.GetByID(badAuthID); !ok {
		t.Fatalf("expected unauthorized auth %q to remain registered when delete-unauthorized-auth=false", badAuthID)
	}
	if deleted := store.DeletedIDs(); len(deleted) != 0 {
		t.Fatalf("store.Delete should not be called when delete-unauthorized-auth=false, got %v", deleted)
	}
}

func TestManager_ModelSupportBadRequest_FallsBackAndSuspendsAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"aa-bad-auth": &Error{
				HTTPStatus: http.StatusBadRequest,
				Message:    "invalid_request_error: The requested model is not supported.",
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "claude-opus-4-6"
	badAuth := &Auth{ID: "aa-bad-auth", Provider: "claude"}
	goodAuth := &Auth{ID: "bb-good-auth", Provider: "claude"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(badAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(goodAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(badAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), badAuth); errRegister != nil {
		t.Fatalf("register bad auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), goodAuth); errRegister != nil {
		t.Fatalf("register good auth: %v", errRegister)
	}

	request := cliproxyexecutor.Request{Model: model}
	for i := 0; i < 2; i++ {
		resp, errExecute := m.Execute(context.Background(), []string{"claude"}, request, cliproxyexecutor.Options{})
		if errExecute != nil {
			t.Fatalf("execute %d error = %v, want success", i, errExecute)
		}
		if string(resp.Payload) != goodAuth.ID {
			t.Fatalf("execute %d payload = %q, want %q", i, string(resp.Payload), goodAuth.ID)
		}
	}

	got := executor.ExecuteCalls()
	want := []string{badAuth.ID, goodAuth.ID, goodAuth.ID}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d auth = %q, want %q", i, got[i], want[i])
		}
	}

	updatedBad, ok := m.GetByID(badAuth.ID)
	if !ok || updatedBad == nil {
		t.Fatalf("expected bad auth to remain registered")
	}
	state := updatedBad.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state for %q", model)
	}
	if !state.Unavailable {
		t.Fatalf("expected bad auth model state to be unavailable")
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatalf("expected bad auth model state cooldown to be set")
	}
}

func TestManager_ModelSupportUnauthorized_FallsBackWithoutEvictingAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "codex",
		executeErrors: map[string]error{
			"aa-bad-auth": &Error{
				HTTPStatus: http.StatusUnauthorized,
				Message:    "unauthorized: The requested model is not supported for this account.",
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "gpt-5.2"
	badAuth := &Auth{ID: "aa-bad-auth", Provider: "codex"}
	goodAuth := &Auth{ID: "bb-good-auth", Provider: "codex"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(badAuth.ID, "codex", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(goodAuth.ID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(badAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), badAuth); errRegister != nil {
		t.Fatalf("register bad auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), goodAuth); errRegister != nil {
		t.Fatalf("register good auth: %v", errRegister)
	}

	request := cliproxyexecutor.Request{Model: model}
	for i := 0; i < 2; i++ {
		resp, errExecute := m.Execute(context.Background(), []string{"codex"}, request, cliproxyexecutor.Options{})
		if errExecute != nil {
			t.Fatalf("execute %d error = %v, want success", i, errExecute)
		}
		if string(resp.Payload) != goodAuth.ID {
			t.Fatalf("execute %d payload = %q, want %q", i, string(resp.Payload), goodAuth.ID)
		}
	}

	got := executor.ExecuteCalls()
	want := []string{badAuth.ID, goodAuth.ID, badAuth.ID, goodAuth.ID}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d auth = %q, want %q", i, got[i], want[i])
		}
	}

	updatedBad, ok := m.GetByID(badAuth.ID)
	if !ok || updatedBad == nil {
		t.Fatalf("expected bad auth to remain registered")
	}
	state := updatedBad.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state for %q", model)
	}
	if state.Status != StatusActive {
		t.Fatalf("expected codex bad auth model state status %q, got %q", StatusActive, state.Status)
	}
	if state.Unavailable {
		t.Fatalf("expected codex bad auth model state to stay available")
	}
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected codex bad auth model cooldown to stay empty, got %v", state.NextRetryAfter)
	}
}

func TestManagerExecuteStream_ModelSupportBadRequestFallsBackAndSuspendsAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		streamFirstErrors: map[string]error{
			"aa-bad-auth": &Error{
				HTTPStatus: http.StatusBadRequest,
				Message:    "invalid_request_error: The requested model is not supported.",
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "claude-opus-4-6"
	badAuth := &Auth{ID: "aa-bad-auth", Provider: "claude"}
	goodAuth := &Auth{ID: "bb-good-auth", Provider: "claude"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(badAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(goodAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(badAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), badAuth); errRegister != nil {
		t.Fatalf("register bad auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), goodAuth); errRegister != nil {
		t.Fatalf("register good auth: %v", errRegister)
	}

	request := cliproxyexecutor.Request{Model: model}
	for i := 0; i < 2; i++ {
		streamResult, errExecute := m.ExecuteStream(context.Background(), []string{"claude"}, request, cliproxyexecutor.Options{})
		if errExecute != nil {
			t.Fatalf("execute stream %d error = %v, want success", i, errExecute)
		}
		var payload []byte
		for chunk := range streamResult.Chunks {
			if chunk.Err != nil {
				t.Fatalf("execute stream %d chunk error = %v, want success", i, chunk.Err)
			}
			payload = append(payload, chunk.Payload...)
		}
		if string(payload) != goodAuth.ID {
			t.Fatalf("execute stream %d payload = %q, want %q", i, string(payload), goodAuth.ID)
		}
	}

	got := executor.StreamCalls()
	want := []string{badAuth.ID, goodAuth.ID, goodAuth.ID}
	if len(got) != len(want) {
		t.Fatalf("stream calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stream call %d auth = %q, want %q", i, got[i], want[i])
		}
	}

	updatedBad, ok := m.GetByID(badAuth.ID)
	if !ok || updatedBad == nil {
		t.Fatalf("expected bad auth to remain registered")
	}
	state := updatedBad.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state for %q", model)
	}
	if !state.Unavailable {
		t.Fatalf("expected bad auth model state to be unavailable")
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatalf("expected bad auth model state cooldown to be set")
	}
}

func TestManager_RequestScopedFeatureUnsupportedBadRequest_FallsBackWithoutSuspendingAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"aa-bad-auth": &Error{
				HTTPStatus: http.StatusBadRequest,
				Message:    "request_feature_unsupported: minimax anthropic compatibility does not support output_config.format",
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "claude-sonnet-4-6"
	badAuth := &Auth{ID: "aa-bad-auth", Provider: "claude"}
	goodAuth := &Auth{ID: "bb-good-auth", Provider: "claude"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(badAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(goodAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(badAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), badAuth); errRegister != nil {
		t.Fatalf("register bad auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), goodAuth); errRegister != nil {
		t.Fatalf("register good auth: %v", errRegister)
	}

	resp, errExecute := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error = %v, want fallback success", errExecute)
	}
	if string(resp.Payload) != goodAuth.ID {
		t.Fatalf("payload = %q, want %q", string(resp.Payload), goodAuth.ID)
	}

	got := executor.ExecuteCalls()
	want := []string{badAuth.ID, goodAuth.ID}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d auth = %q, want %q", i, got[i], want[i])
		}
	}

	updatedBad, ok := m.GetByID(badAuth.ID)
	if !ok || updatedBad == nil {
		t.Fatalf("expected bad auth to remain registered")
	}
	if state := updatedBad.ModelStates[model]; state != nil {
		t.Fatalf("expected request-scoped feature incompatibility to avoid model suspension, got state=%+v", *state)
	}
}

func TestManager_MarkResult_RespectsAuthDisableCoolingOverride(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)

	auth := &Auth{
		ID:       "auth-1",
		Provider: "claude",
		Metadata: map[string]any{
			"disable_cooling": true,
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "test-model"
	m.MarkResult(context.Background(), Result{
		AuthID:   "auth-1",
		Provider: "claude",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: 500, Message: "boom"},
	})

	updated, ok := m.GetByID("auth-1")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter to be zero when disable_cooling=true, got %v", state.NextRetryAfter)
	}
}

func TestManager_MarkResult_RespectsAuthDisableCoolingOverride_On403(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)

	auth := &Auth{
		ID:       "auth-403",
		Provider: "claude",
		Metadata: map[string]any{
			"disable_cooling": true,
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "test-model-403"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "claude",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusForbidden, Message: "forbidden"},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter to be zero when disable_cooling=true, got %v", state.NextRetryAfter)
	}

	if count := reg.GetModelCount(model); count <= 0 {
		t.Fatalf("expected model count > 0 when disable_cooling=true, got %d", count)
	}
}

func TestManager_Execute_DisableCooling_DoesNotBlackoutAfter403(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"auth-403-exec": &Error{
				HTTPStatus: http.StatusForbidden,
				Message:    "forbidden",
			},
		},
	}
	m.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "auth-403-exec",
		Provider: "claude",
		Metadata: map[string]any{
			"disable_cooling": true,
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "test-model-403-exec"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	req := cliproxyexecutor.Request{Model: model}
	_, errExecute1 := m.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{})
	if errExecute1 == nil {
		t.Fatal("expected first execute error")
	}
	if statusCodeFromError(errExecute1) != http.StatusForbidden {
		t.Fatalf("first execute status = %d, want %d", statusCodeFromError(errExecute1), http.StatusForbidden)
	}

	_, errExecute2 := m.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{})
	if errExecute2 == nil {
		t.Fatal("expected second execute error")
	}
	if statusCodeFromError(errExecute2) != http.StatusForbidden {
		t.Fatalf("second execute status = %d, want %d", statusCodeFromError(errExecute2), http.StatusForbidden)
	}
}

func TestManager_Execute_DisableCooling_DoesNotBlackoutAfter429RetryAfter(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"auth-429-exec": &retryAfterStatusError{
				status:     http.StatusTooManyRequests,
				message:    "quota exhausted",
				retryAfter: 2 * time.Minute,
			},
		},
	}
	m.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "auth-429-exec",
		Provider: "claude",
		Metadata: map[string]any{
			"disable_cooling": true,
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "test-model-429-exec"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	req := cliproxyexecutor.Request{Model: model}
	_, errExecute1 := m.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{})
	if errExecute1 == nil {
		t.Fatal("expected first execute error")
	}
	if statusCodeFromError(errExecute1) != http.StatusTooManyRequests {
		t.Fatalf("first execute status = %d, want %d", statusCodeFromError(errExecute1), http.StatusTooManyRequests)
	}

	_, errExecute2 := m.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{})
	if errExecute2 == nil {
		t.Fatal("expected second execute error")
	}
	if statusCodeFromError(errExecute2) != http.StatusTooManyRequests {
		t.Fatalf("second execute status = %d, want %d", statusCodeFromError(errExecute2), http.StatusTooManyRequests)
	}

	calls := executor.ExecuteCalls()
	if len(calls) != 2 {
		t.Fatalf("execute calls = %d, want 2", len(calls))
	}

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter to be zero when disable_cooling=true, got %v", state.NextRetryAfter)
	}
}

func TestManager_Execute_DisableCooling_RetriesAfter429RetryAfter(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(3, 100*time.Millisecond, 0)

	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"auth-429-retryafter-exec": &retryAfterStatusError{
				status:     http.StatusTooManyRequests,
				message:    "quota exhausted",
				retryAfter: 5 * time.Millisecond,
			},
		},
	}
	m.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "auth-429-retryafter-exec",
		Provider: "claude",
		Metadata: map[string]any{
			"disable_cooling": true,
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "test-model-429-retryafter-exec"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	req := cliproxyexecutor.Request{Model: model}
	_, errExecute := m.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected execute error")
	}
	if statusCodeFromError(errExecute) != http.StatusTooManyRequests {
		t.Fatalf("execute status = %d, want %d", statusCodeFromError(errExecute), http.StatusTooManyRequests)
	}

	calls := executor.ExecuteCalls()
	if len(calls) != 4 {
		t.Fatalf("execute calls = %d, want 4 (initial + 3 retries)", len(calls))
	}
}

func TestManager_MarkResult_RequestScopedNotFoundDoesNotCooldownAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)

	auth := &Auth{
		ID:       "auth-1",
		Provider: "openai",
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "gpt-4.1"
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  false,
		Error: &Error{
			HTTPStatus: http.StatusNotFound,
			Message:    requestScopedNotFoundMessage,
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if updated.Unavailable {
		t.Fatalf("expected request-scoped 404 to keep auth available")
	}
	if !updated.NextRetryAfter.IsZero() {
		t.Fatalf("expected request-scoped 404 to keep auth cooldown unset, got %v", updated.NextRetryAfter)
	}
	if state := updated.ModelStates[model]; state != nil {
		t.Fatalf("expected request-scoped 404 to avoid model cooldown state, got %#v", state)
	}
}

func TestManager_MarkResult_RequestScopedContentSafetyDoesNotCooldownAuth(t *testing.T) {
	tests := []struct {
		name       string
		code       string
		httpStatus int
		message    string
	}{
		{
			name:       "high risk bad request",
			httpStatus: http.StatusBadRequest,
			message:    requestScopedContentSafetyMessage,
		},
		{
			name:       "blocked legal reasons",
			httpStatus: http.StatusUnavailableForLegalReasons,
			message:    requestScopedContentBlockedMessage,
		},
		{
			name:       "blocked status in message",
			httpStatus: 0,
			message:    "status_code=451, " + requestScopedContentBlockedMessage,
		},
		{
			name:       "minimax new sensitive internal server error",
			code:       "1026",
			httpStatus: http.StatusInternalServerError,
			message:    miniMaxNewSensitiveMessage,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)

			auth := &Auth{
				ID:       "auth-1",
				Provider: "claude",
			}
			if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}

			model := "kimi-k2.6"
			m.MarkResult(context.Background(), Result{
				AuthID:   auth.ID,
				Provider: auth.Provider,
				Model:    model,
				Success:  false,
				Error: &Error{
					Code:       tt.code,
					HTTPStatus: tt.httpStatus,
					Message:    tt.message,
				},
			})

			updated, ok := m.GetByID(auth.ID)
			if !ok || updated == nil {
				t.Fatalf("expected auth to be present")
			}
			if updated.Unavailable {
				t.Fatalf("expected request-scoped content safety error to keep auth available")
			}
			if !updated.NextRetryAfter.IsZero() {
				t.Fatalf("expected request-scoped content safety error to keep auth cooldown unset, got %v", updated.NextRetryAfter)
			}
			if state := updated.ModelStates[model]; state != nil {
				t.Fatalf("expected request-scoped content safety error to avoid model cooldown state, got %#v", state)
			}
		})
	}
}

func TestRequestScopedContentSafetyStopsRetry(t *testing.T) {
	tests := []struct {
		name       string
		code       string
		httpStatus int
		message    string
	}{
		{
			name:       "high risk bad request",
			httpStatus: http.StatusBadRequest,
			message:    requestScopedContentSafetyMessage,
		},
		{
			name:       "blocked legal reasons",
			httpStatus: http.StatusUnavailableForLegalReasons,
			message:    requestScopedContentBlockedMessage,
		},
		{
			name:       "blocked status in message",
			httpStatus: 0,
			message:    "status_code=451, " + requestScopedContentBlockedMessage,
		},
		{
			name:       "minimax new sensitive internal server error",
			code:       "1026",
			httpStatus: http.StatusInternalServerError,
			message:    miniMaxNewSensitiveMessage,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &Error{
				Code:       tt.code,
				HTTPStatus: tt.httpStatus,
				Message:    tt.message,
			}
			if !isRequestInvalidError(err) {
				t.Fatalf("expected content safety error to be request invalid")
			}
		})
	}
}

func TestMiniMaxNewSensitiveFallsBackForConfiguredRouteModels(t *testing.T) {
	err := &Error{
		Code:       "1026",
		HTTPStatus: http.StatusInternalServerError,
		Message:    miniMaxNewSensitiveMessage,
	}
	if !isRequestInvalidError(err) {
		t.Fatal("expected MiniMax new_sensitive to be request invalid")
	}
	for _, model := range []string{"claude-sonnet-4-6", "glm-4.7"} {
		if !shouldFallbackRequestScopedRouteErrorForRequest(model, cliproxyexecutor.Options{}, err) {
			t.Fatalf("expected MiniMax new_sensitive to fallback for %s", model)
		}
	}
}

func TestMiniMaxOutputNewSensitiveDoesNotFallbackForConfiguredRouteModels(t *testing.T) {
	err := &Error{
		Code:       "1027",
		HTTPStatus: http.StatusInternalServerError,
		Message:    miniMaxOutputNewSensitiveMessage,
	}
	if !isRequestInvalidError(err) {
		t.Fatal("expected MiniMax output new_sensitive to be request invalid")
	}
	for _, model := range []string{"claude-sonnet-4-6", "glm-4.7"} {
		if shouldFallbackRequestScopedRouteErrorForRequest(model, cliproxyexecutor.Options{}, err) {
			t.Fatalf("expected MiniMax output new_sensitive to stop fallback for %s", model)
		}
	}
}

func TestManager_Execute_ClaudeSonnetAliasContentSafetyFallsBack(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"aa-blocked-auth": &Error{
				Message: "status_code=451, " + requestScopedContentBlockedMessage,
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "claude-sonnet-4-6"
	blockedAuth := &Auth{ID: "aa-blocked-auth", Provider: "claude"}
	goodAuth := &Auth{ID: "bb-good-auth", Provider: "claude"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(blockedAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(goodAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(blockedAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), blockedAuth); errRegister != nil {
		t.Fatalf("register blocked auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), goodAuth); errRegister != nil {
		t.Fatalf("register good auth: %v", errRegister)
	}

	resp, errExecute := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success", errExecute)
	}
	if string(resp.Payload) != goodAuth.ID {
		t.Fatalf("execute payload = %q, want %q", string(resp.Payload), goodAuth.ID)
	}
	got := executor.ExecuteCalls()
	want := []string{blockedAuth.ID, goodAuth.ID}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d auth = %q, want %q", i, got[i], want[i])
		}
	}

	updatedBlocked, ok := m.GetByID(blockedAuth.ID)
	if !ok || updatedBlocked == nil {
		t.Fatalf("expected blocked auth to remain registered")
	}
	if updatedBlocked.Unavailable {
		t.Fatalf("expected content safety fallback to keep blocked auth available")
	}
	if state := updatedBlocked.ModelStates[model]; state != nil {
		t.Fatalf("expected content safety fallback to avoid model cooldown state, got %#v", state)
	}
}

func TestManager_Execute_ClaudeSonnetAliasContentSafetyIgnoresCredentialLimit(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(0, 0, 1)
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"aa-blocked-auth": &Error{
				HTTPStatus: http.StatusUnavailableForLegalReasons,
				Message:    requestScopedContentBlockedMessage,
			},
			"bb-blocked-auth": &Error{
				HTTPStatus: http.StatusUnavailableForLegalReasons,
				Message:    requestScopedContentBlockedMessage,
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "claude-sonnet-4-6"
	blockedAuthA := &Auth{ID: "aa-blocked-auth", Provider: "claude"}
	blockedAuthB := &Auth{ID: "bb-blocked-auth", Provider: "claude"}
	goodAuth := &Auth{ID: "cc-good-auth", Provider: "claude"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(blockedAuthA.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(blockedAuthB.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(goodAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(blockedAuthA.ID)
		reg.UnregisterClient(blockedAuthB.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), blockedAuthA); errRegister != nil {
		t.Fatalf("register first blocked auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), blockedAuthB); errRegister != nil {
		t.Fatalf("register second blocked auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), goodAuth); errRegister != nil {
		t.Fatalf("register good auth: %v", errRegister)
	}

	resp, errExecute := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success", errExecute)
	}
	if string(resp.Payload) != goodAuth.ID {
		t.Fatalf("execute payload = %q, want %q", string(resp.Payload), goodAuth.ID)
	}
	got := executor.ExecuteCalls()
	want := []string{blockedAuthA.ID, blockedAuthB.ID, goodAuth.ID}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d auth = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManager_Execute_ClaudeSonnetAliasMetadataContentSafetyFallsBack(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"aa-blocked-auth": &Error{
				HTTPStatus: http.StatusUnavailableForLegalReasons,
				Message:    requestScopedContentBlockedMessage,
			},
		},
	}
	m.RegisterExecutor(executor)

	routeModel := "step-3.7-flash"
	blockedAuth := &Auth{ID: "aa-blocked-auth", Provider: "claude"}
	goodAuth := &Auth{ID: "bb-good-auth", Provider: "claude"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(blockedAuth.ID, "claude", []*registry.ModelInfo{{ID: routeModel}})
	reg.RegisterClient(goodAuth.ID, "claude", []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(blockedAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), blockedAuth); errRegister != nil {
		t.Fatalf("register blocked auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), goodAuth); errRegister != nil {
		t.Fatalf("register good auth: %v", errRegister)
	}

	resp, errExecute := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: "claude-sonnet-4-6",
		},
	})
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success", errExecute)
	}
	if string(resp.Payload) != goodAuth.ID {
		t.Fatalf("execute payload = %q, want %q", string(resp.Payload), goodAuth.ID)
	}
	got := executor.ExecuteCalls()
	want := []string{blockedAuth.ID, goodAuth.ID}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d auth = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManager_Execute_GenericContentSafetyStillStopsRetry(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"aa-blocked-auth": &Error{
				HTTPStatus: http.StatusUnavailableForLegalReasons,
				Message:    requestScopedContentBlockedMessage,
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "claude-opus-4-6"
	blockedAuth := &Auth{ID: "aa-blocked-auth", Provider: "claude"}
	goodAuth := &Auth{ID: "bb-good-auth", Provider: "claude"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(blockedAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(goodAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(blockedAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), blockedAuth); errRegister != nil {
		t.Fatalf("register blocked auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), goodAuth); errRegister != nil {
		t.Fatalf("register good auth: %v", errRegister)
	}

	_, errExecute := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected content safety error")
	}
	if statusCodeFromError(errExecute) != http.StatusUnavailableForLegalReasons {
		t.Fatalf("status = %d, want %d", statusCodeFromError(errExecute), http.StatusUnavailableForLegalReasons)
	}
	got := executor.ExecuteCalls()
	want := []string{blockedAuth.ID}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d auth = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManager_Execute_ClaudeSonnetAliasContextLimitFallsBackWithoutCooldown(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(0, 0, 1)
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"aa-minimax-auth": &Error{
				Message: "status_code=400, " + requestScopedContextLimitMessage,
			},
			"bb-minimax-auth": &Error{
				HTTPStatus: http.StatusBadRequest,
				Message:    requestScopedContextLimitMessage,
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "claude-sonnet-4-6"
	minimaxAuthA := &Auth{ID: "aa-minimax-auth", Provider: "claude"}
	minimaxAuthB := &Auth{ID: "bb-minimax-auth", Provider: "claude"}
	stepAuth := &Auth{ID: "cc-step-auth", Provider: "claude"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(minimaxAuthA.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(minimaxAuthB.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(stepAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(minimaxAuthA.ID)
		reg.UnregisterClient(minimaxAuthB.ID)
		reg.UnregisterClient(stepAuth.ID)
	})

	for _, auth := range []*Auth{minimaxAuthA, minimaxAuthB, stepAuth} {
		if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth %s: %v", auth.ID, errRegister)
		}
	}

	resp, errExecute := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success", errExecute)
	}
	if string(resp.Payload) != stepAuth.ID {
		t.Fatalf("execute payload = %q, want %q", string(resp.Payload), stepAuth.ID)
	}
	got := executor.ExecuteCalls()
	want := []string{minimaxAuthA.ID, minimaxAuthB.ID, stepAuth.ID}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d auth = %q, want %q", i, got[i], want[i])
		}
	}
	for _, authID := range []string{minimaxAuthA.ID, minimaxAuthB.ID} {
		updated, ok := m.GetByID(authID)
		if !ok || updated == nil {
			t.Fatalf("expected auth %s to remain registered", authID)
		}
		if updated.Unavailable {
			t.Fatalf("expected context-limit fallback to keep auth %s available", authID)
		}
		if state := updated.ModelStates[model]; state != nil {
			t.Fatalf("expected context-limit fallback to avoid model cooldown state for %s, got %#v", authID, state)
		}
	}
}

func TestManager_Execute_GenericContextLimitStillStopsRetry(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"aa-minimax-auth": &Error{
				HTTPStatus: http.StatusBadRequest,
				Message:    requestScopedContextLimitMessage,
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "claude-opus-4-6"
	minimaxAuth := &Auth{ID: "aa-minimax-auth", Provider: "claude"}
	stepAuth := &Auth{ID: "bb-step-auth", Provider: "claude"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(minimaxAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(stepAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(minimaxAuth.ID)
		reg.UnregisterClient(stepAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), minimaxAuth); errRegister != nil {
		t.Fatalf("register minimax auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), stepAuth); errRegister != nil {
		t.Fatalf("register step auth: %v", errRegister)
	}

	_, errExecute := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected context-limit error")
	}
	if statusCodeFromError(errExecute) != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", statusCodeFromError(errExecute), http.StatusBadRequest)
	}
	got := executor.ExecuteCalls()
	want := []string{minimaxAuth.ID}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d auth = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManager_Execute_ClaudeSonnetAliasMetadataContextLimitFallsBack(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"aa-minimax-auth": &Error{
				HTTPStatus: http.StatusBadRequest,
				Message:    requestScopedContextLimitMessage,
			},
		},
	}
	m.RegisterExecutor(executor)

	routeModel := "minimax-m2.7-highspeed"
	minimaxAuth := &Auth{ID: "aa-minimax-auth", Provider: "claude"}
	stepAuth := &Auth{ID: "bb-step-auth", Provider: "claude"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(minimaxAuth.ID, "claude", []*registry.ModelInfo{{ID: routeModel}})
	reg.RegisterClient(stepAuth.ID, "claude", []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(minimaxAuth.ID)
		reg.UnregisterClient(stepAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), minimaxAuth); errRegister != nil {
		t.Fatalf("register minimax auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), stepAuth); errRegister != nil {
		t.Fatalf("register step auth: %v", errRegister)
	}

	resp, errExecute := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: "claude-sonnet-4-6",
		},
	})
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success", errExecute)
	}
	if string(resp.Payload) != stepAuth.ID {
		t.Fatalf("execute payload = %q, want %q", string(resp.Payload), stepAuth.ID)
	}
	got := executor.ExecuteCalls()
	want := []string{minimaxAuth.ID, stepAuth.ID}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d auth = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManager_Execute_GLMAliasMetadataMiniMaxNewSensitiveFallsBack(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"aa-minimax-auth": &Error{
				Code:       "1026",
				HTTPStatus: http.StatusInternalServerError,
				Message:    miniMaxNewSensitiveMessage,
			},
		},
	}
	m.RegisterExecutor(executor)

	routeModel := "step-3.7-flash"
	minimaxAuth := &Auth{ID: "aa-minimax-auth", Provider: "claude"}
	stepAuth := &Auth{ID: "bb-step-auth", Provider: "claude"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(minimaxAuth.ID, "claude", []*registry.ModelInfo{{ID: routeModel}})
	reg.RegisterClient(stepAuth.ID, "claude", []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(minimaxAuth.ID)
		reg.UnregisterClient(stepAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), minimaxAuth); errRegister != nil {
		t.Fatalf("register minimax auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), stepAuth); errRegister != nil {
		t.Fatalf("register step auth: %v", errRegister)
	}

	resp, errExecute := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: "glm-4.7",
		},
	})
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success", errExecute)
	}
	if string(resp.Payload) != stepAuth.ID {
		t.Fatalf("execute payload = %q, want %q", string(resp.Payload), stepAuth.ID)
	}
	got := executor.ExecuteCalls()
	want := []string{minimaxAuth.ID, stepAuth.ID}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d auth = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManager_Execute_GLMAliasMetadataMiniMaxOutputNewSensitiveStops(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"aa-minimax-auth": &Error{
				Code:       "1027",
				HTTPStatus: http.StatusInternalServerError,
				Message:    miniMaxOutputNewSensitiveMessage,
			},
		},
	}
	m.RegisterExecutor(executor)

	routeModel := "step-3.7-flash"
	minimaxAuth := &Auth{ID: "aa-minimax-auth", Provider: "claude"}
	stepAuth := &Auth{ID: "bb-step-auth", Provider: "claude"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(minimaxAuth.ID, "claude", []*registry.ModelInfo{{ID: routeModel}})
	reg.RegisterClient(stepAuth.ID, "claude", []*registry.ModelInfo{{ID: routeModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(minimaxAuth.ID)
		reg.UnregisterClient(stepAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), minimaxAuth); errRegister != nil {
		t.Fatalf("register minimax auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), stepAuth); errRegister != nil {
		t.Fatalf("register step auth: %v", errRegister)
	}

	_, errExecute := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: "glm-4.7",
		},
	})
	if errExecute == nil {
		t.Fatal("expected MiniMax output new_sensitive error")
	}
	if code := errorCodeFromError(errExecute); code != "1027" {
		t.Fatalf("error code = %q, want 1027", code)
	}
	got := executor.ExecuteCalls()
	want := []string{minimaxAuth.ID}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d auth = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManager_ExecuteCount_ClaudeSonnetAliasContentSafetyFallsBack(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		countErrors: map[string]error{
			"aa-blocked-auth": &Error{
				HTTPStatus: http.StatusUnavailableForLegalReasons,
				Message:    requestScopedContentBlockedMessage,
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "claude-sonnet-4-6"
	blockedAuth := &Auth{ID: "aa-blocked-auth", Provider: "claude"}
	goodAuth := &Auth{ID: "bb-good-auth", Provider: "claude"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(blockedAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(goodAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(blockedAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), blockedAuth); errRegister != nil {
		t.Fatalf("register blocked auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), goodAuth); errRegister != nil {
		t.Fatalf("register good auth: %v", errRegister)
	}

	resp, errExecute := m.ExecuteCount(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute count error = %v, want success", errExecute)
	}
	if string(resp.Payload) != goodAuth.ID {
		t.Fatalf("execute count payload = %q, want %q", string(resp.Payload), goodAuth.ID)
	}
	got := executor.CountCalls()
	want := []string{blockedAuth.ID, goodAuth.ID}
	if len(got) != len(want) {
		t.Fatalf("count calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("count call %d auth = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManager_ExecuteStream_ClaudeSonnetAliasContentSafetyFallsBack(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		streamFirstErrors: map[string]error{
			"aa-blocked-auth": &Error{
				HTTPStatus: http.StatusUnavailableForLegalReasons,
				Message:    requestScopedContentBlockedMessage,
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "claude-sonnet-4-6"
	blockedAuth := &Auth{ID: "aa-blocked-auth", Provider: "claude"}
	goodAuth := &Auth{ID: "bb-good-auth", Provider: "claude"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(blockedAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(goodAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(blockedAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), blockedAuth); errRegister != nil {
		t.Fatalf("register blocked auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), goodAuth); errRegister != nil {
		t.Fatalf("register good auth: %v", errRegister)
	}

	streamResult, errExecute := m.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute stream error = %v, want success", errExecute)
	}
	var payload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("execute stream chunk error = %v, want success", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if string(payload) != goodAuth.ID {
		t.Fatalf("execute stream payload = %q, want %q", string(payload), goodAuth.ID)
	}
	got := executor.StreamCalls()
	want := []string{blockedAuth.ID, goodAuth.ID}
	if len(got) != len(want) {
		t.Fatalf("stream calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stream call %d auth = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManager_RequestScopedNotFoundStopsRetryWithoutSuspendingAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "openai",
		executeErrors: map[string]error{
			"aa-bad-auth": &Error{
				HTTPStatus: http.StatusNotFound,
				Message:    requestScopedNotFoundMessage,
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "gpt-4.1"
	badAuth := &Auth{ID: "aa-bad-auth", Provider: "openai"}
	goodAuth := &Auth{ID: "bb-good-auth", Provider: "openai"}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(badAuth.ID, "openai", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(goodAuth.ID, "openai", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(badAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), badAuth); errRegister != nil {
		t.Fatalf("register bad auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), goodAuth); errRegister != nil {
		t.Fatalf("register good auth: %v", errRegister)
	}

	_, errExecute := m.Execute(context.Background(), []string{"openai"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected request-scoped not-found error")
	}
	errResult, ok := errExecute.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T", errExecute)
	}
	if errResult.HTTPStatus != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", errResult.HTTPStatus, http.StatusNotFound)
	}
	if errResult.Message != requestScopedNotFoundMessage {
		t.Fatalf("message = %q, want %q", errResult.Message, requestScopedNotFoundMessage)
	}

	got := executor.ExecuteCalls()
	want := []string{badAuth.ID}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d auth = %q, want %q", i, got[i], want[i])
		}
	}

	updatedBad, ok := m.GetByID(badAuth.ID)
	if !ok || updatedBad == nil {
		t.Fatalf("expected bad auth to remain registered")
	}
	if updatedBad.Unavailable {
		t.Fatalf("expected request-scoped 404 to keep bad auth available")
	}
	if !updatedBad.NextRetryAfter.IsZero() {
		t.Fatalf("expected request-scoped 404 to keep bad auth cooldown unset, got %v", updatedBad.NextRetryAfter)
	}
	if state := updatedBad.ModelStates[model]; state != nil {
		t.Fatalf("expected request-scoped 404 to avoid bad auth model cooldown state, got %#v", state)
	}
}
