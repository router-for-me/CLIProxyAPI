package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const claudeExtraUsageBody = `{"type":"error","error":{"type":"invalid_request_error","message":"You're out of extra usage. Add more at claude.ai/settings/usage and keep going."}}`

type requestScopedResponseBodyError struct {
	status int
	body   string
}

func (e requestScopedResponseBodyError) Error() string         { return "claude Fast upstream request failed" }
func (e requestScopedResponseBodyError) StatusCode() int       { return e.status }
func (e requestScopedResponseBodyError) ResponseBody() []byte  { return []byte(e.body) }
func (e requestScopedResponseBodyError) IsRequestScoped() bool { return true }

func TestIsRequestInvalidError_ClaudeExtraUsageIsNotRequestFault(t *testing.T) {
	extraUsageErr := &Error{HTTPStatus: http.StatusBadRequest, Message: claudeExtraUsageBody}
	if isRequestInvalidError(extraUsageErr) {
		t.Fatal("extra-usage 400 must not be request-invalid")
	}
	if !isOutOfExtraUsageError(extraUsageErr) {
		t.Fatal("expected isOutOfExtraUsageError to match Anthropic extra-usage body")
	}

	genericErr := &Error{
		HTTPStatus: http.StatusBadRequest,
		Message:    `{"error":{"type":"invalid_request_error","message":"Invalid request parameter"}}`,
	}
	if !isRequestInvalidError(genericErr) {
		t.Fatal("generic invalid_request_error 400 must remain request-invalid")
	}
	if isOutOfExtraUsageError(genericErr) {
		t.Fatal("generic invalid_request_error must not match extra-usage")
	}

	extraFieldErr := &Error{
		HTTPStatus: http.StatusBadRequest,
		Message:    `{"error":{"type":"invalid_request_error","message":"Unexpected extra field 'foo'"}}`,
	}
	if !isRequestInvalidError(extraFieldErr) {
		t.Fatal("unexpected extra field 400 must remain request-invalid")
	}

	billingPeriodErr := &Error{
		HTTPStatus: http.StatusBadRequest,
		Message:    `{"error":{"type":"invalid_request_error","message":"out of extra usage for this billing period"}}`,
	}
	if isRequestInvalidError(billingPeriodErr) {
		t.Fatal("you're-less extra-usage billing period must not be request-invalid")
	}
	if !isOutOfExtraUsageError(billingPeriodErr) {
		t.Fatal("expected isOutOfExtraUsageError to match billing-period extra-usage variant")
	}
}

func TestResultErrorFromError_ClaudeFastExtraUsagePreservesQuotaBody(t *testing.T) {
	err := requestScopedResponseBodyError{
		status: http.StatusBadRequest,
		body:   claudeExtraUsageBody,
	}
	if isRequestInvalidError(err) {
		t.Fatal("request-scoped wrapper must not mask an extra-usage quota response")
	}

	got := resultErrorFromError(err)
	if got == nil {
		t.Fatal("resultErrorFromError() = nil")
	}
	if got.Code == requestScopedErrorCode {
		t.Fatalf("result code = %q, must not be request-scoped", got.Code)
	}
	if got.Message != claudeExtraUsageBody {
		t.Fatalf("result message = %q, want upstream body", got.Message)
	}
	if !isOutOfExtraUsageResultError(got) {
		t.Fatal("preserved result error must remain classifiable as extra usage")
	}
}

func TestManager_Execute_ClaudeExtraUsage400CoolsAndRotates(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"aa-extra-usage-auth": requestScopedResponseBodyError{
				status: http.StatusBadRequest,
				body:   claudeExtraUsageBody,
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "claude-opus-4-extra-usage"
	depleted := &Auth{
		ID:         "aa-extra-usage-auth",
		Provider:   "claude",
		Attributes: map[string]string{"auth_kind": "oauth"},
	}
	healthy := &Auth{
		ID:         "bb-extra-usage-auth",
		Provider:   "claude",
		Attributes: map[string]string{"auth_kind": "oauth"},
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(depleted.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(healthy.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(depleted.ID)
		reg.UnregisterClient(healthy.ID)
	})

	if _, errRegister := m.Register(context.Background(), depleted); errRegister != nil {
		t.Fatalf("register depleted: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), healthy); errRegister != nil {
		t.Fatalf("register healthy: %v", errRegister)
	}

	resp, errExecute := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("Execute error = %v, want success via second auth", errExecute)
	}
	if string(resp.Payload) != healthy.ID {
		t.Fatalf("payload = %q, want %q", string(resp.Payload), healthy.ID)
	}

	got := executor.ExecuteCalls()
	want := []string{depleted.ID, healthy.ID}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d auth = %q, want %q", i, got[i], want[i])
		}
	}

	updated, ok := m.GetByID(depleted.ID)
	if !ok || updated == nil {
		t.Fatalf("expected depleted auth to remain registered")
	}
	if !updated.Quota.Exceeded {
		t.Fatalf("expected depleted auth Quota.Exceeded, got %#v", updated.Quota)
	}
	if updated.NextRetryAfter.IsZero() {
		t.Fatalf("expected depleted auth NextRetryAfter to be set")
	}
	if updated.Quota.Reason != "credential_quota" && updated.Quota.Reason != "quota" {
		t.Fatalf("quota reason = %q, want credential_quota or quota", updated.Quota.Reason)
	}
	state := updated.ModelStates[model]
	if state == nil || !state.Quota.Exceeded || state.NextRetryAfter.IsZero() {
		t.Fatalf("expected model quota cooldown, got %#v", state)
	}

	healthyUpdated, ok := m.GetByID(healthy.ID)
	if !ok || healthyUpdated == nil {
		t.Fatalf("expected healthy auth to remain registered")
	}
	if healthyUpdated.Quota.Exceeded || !healthyUpdated.NextRetryAfter.IsZero() {
		t.Fatalf("healthy auth must not be cooled, got quota=%#v next=%v", healthyUpdated.Quota, healthyUpdated.NextRetryAfter)
	}
}

func TestManager_Execute_GenericInvalidRequest400DoesNotRotateAsQuota(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	genericErr := &Error{
		HTTPStatus: http.StatusBadRequest,
		Message:    `{"error":{"type":"invalid_request_error","message":"Unexpected extra field 'foo'"}}`,
	}
	m := NewManager(nil, nil, nil)
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"aa-generic-400-auth": genericErr,
		},
	}
	m.RegisterExecutor(executor)

	model := "claude-opus-4-generic-400"
	faulty := &Auth{
		ID:         "aa-generic-400-auth",
		Provider:   "claude",
		Attributes: map[string]string{"auth_kind": "oauth"},
	}
	healthy := &Auth{
		ID:         "bb-generic-400-auth",
		Provider:   "claude",
		Attributes: map[string]string{"auth_kind": "oauth"},
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(faulty.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(healthy.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(faulty.ID)
		reg.UnregisterClient(healthy.ID)
	})

	if _, errRegister := m.Register(context.Background(), faulty); errRegister != nil {
		t.Fatalf("register faulty: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), healthy); errRegister != nil {
		t.Fatalf("register healthy: %v", errRegister)
	}

	_, errExecute := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected generic 400 to stop as request-fault")
	}
	if !errors.Is(errExecute, genericErr) && errExecute.Error() != genericErr.Error() {
		t.Fatalf("execute error = %v, want generic invalid_request_error", errExecute)
	}

	got := executor.ExecuteCalls()
	if len(got) != 1 || got[0] != faulty.ID {
		t.Fatalf("execute calls = %v, want only first auth", got)
	}

	updated, ok := m.GetByID(faulty.ID)
	if !ok || updated == nil {
		t.Fatalf("expected faulty auth to remain registered")
	}
	if updated.Quota.Exceeded {
		t.Fatalf("generic 400 must not set Quota.Exceeded")
	}
	if !updated.NextRetryAfter.IsZero() {
		t.Fatalf("generic 400 must not cool auth, NextRetryAfter=%v", updated.NextRetryAfter)
	}
	if state := updated.ModelStates[model]; state != nil && (state.Quota.Exceeded || !state.NextRetryAfter.IsZero()) {
		t.Fatalf("generic 400 must not cool model, got %#v", state)
	}
}
