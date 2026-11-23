package auth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const quotaTestModel = "claude-quota-recheck-opus"
const quotaTestSibling = "claude-quota-recheck-sibling"

func markClaudeWeeklyQuota(m *Manager, id string) {
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, http.Header{
		"Anthropic-Ratelimit-Unified-Status":    {"rejected"},
		"Anthropic-Ratelimit-Unified-7d-Status": {"rejected"},
	})
	retry := 12 * time.Hour
	m.MarkResult(ctx, Result{
		AuthID: id, Provider: "claude", Model: quotaTestModel, CredentialScope: true,
		RetryAfter: &retry, ExecutionSeq: m.nextExecutionSeq(),
		Error: &Error{Code: ErrorCodeCredentialQuota, HTTPStatus: http.StatusTooManyRequests},
	})
}

func claudeQuotaTestManager(t *testing.T, executor ProviderExecutor) *Manager {
	t.Helper()
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)
	registry.GetGlobalRegistry().RegisterClient(t.Name(), "claude", []*registry.ModelInfo{{ID: quotaTestModel}, {ID: quotaTestSibling}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(t.Name()) })
	if _, err := m.Register(context.Background(), &Auth{
		ID: t.Name(), Provider: "claude", Status: StatusActive,
		Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
	}); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestVerifyInferenceReleasesClaudeCredentialQuota(t *testing.T) {
	withQuotaCooldownEnabled(t)
	ctx := context.Background()
	m := claudeQuotaTestManager(t, &issue7737VerifyExecutor{id: "claude"})
	siblingRetry := time.Hour
	m.MarkResult(ctx, Result{
		AuthID: t.Name(), Provider: "claude", Model: quotaTestSibling, RetryAfter: &siblingRetry,
		Error: &Error{Code: "rate_limit_error", HTTPStatus: http.StatusTooManyRequests},
	})
	markClaudeWeeklyQuota(m, t.Name())
	if _, err := m.Execute(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: quotaTestModel}, cliproxyexecutor.Options{}); err == nil {
		t.Fatal("routing succeeded before quota recheck")
	}

	verified, err := m.VerifyInference(ctx, t.Name(), quotaTestModel)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Quota.Reason == "credential_quota" || !verified.InferenceVerified {
		t.Fatal("completed verification did not restore credential routing")
	}
	if _, err := m.Execute(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: quotaTestModel}, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("routing still blocked after verification: %v", err)
	}
	if _, err := m.Execute(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: quotaTestSibling}, cliproxyexecutor.Options{}); err == nil {
		t.Fatal("independent sibling cooldown was cleared")
	}
}

func TestVerifyInferenceKeepsNewerClaudeCredentialQuota(t *testing.T) {
	withQuotaCooldownEnabled(t)
	executor := &issue7737VerifyExecutor{id: "claude"}
	m := claudeQuotaTestManager(t, executor)
	markClaudeWeeklyQuota(m, t.Name())
	executor.onStream = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
		markClaudeWeeklyQuota(m, t.Name())
		return &cliproxyexecutor.StreamResult{Chunks: issue7737CompletedStream()}, nil
	}
	if _, err := m.VerifyInference(context.Background(), t.Name(), quotaTestModel); err == nil {
		t.Fatal("verification cleared newer quota rejection")
	}
	stored, _ := m.GetByID(t.Name())
	if stored.RecoveryStageAt(time.Now()) != RecoveryStageQuotaBlocked {
		t.Fatal("newer quota rejection was lost")
	}
}

type claudeQuotaRecheckExecutor struct {
	*issue7737VerifyExecutor
	t          *testing.T
	usage      string
	usageCalls int
}

func (e *claudeQuotaRecheckExecutor) HttpRequest(_ context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	e.usageCalls++
	if auth.ID != e.t.Name() || req.Method != http.MethodGet || req.URL.String() != "https://api.anthropic.com/api/oauth/usage" {
		e.t.Fatal("unexpected credential or request for Claude usage check")
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(e.usage))}, nil
}

func TestClaudeQuotaRecheckUsesFreshUsageAndRequiresInference(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, tc := range []struct {
		name           string
		usage          string
		inferenceFails bool
		wantProbe      bool
		wantBlocked    bool
	}{
		{name: "headroom", usage: `{"five_hour":{"utilization":5},"seven_day":{"utilization":93}}`, wantProbe: true},
		{name: "still_exhausted", usage: `{"five_hour":{"utilization":5},"seven_day":{"utilization":100}}`, wantBlocked: true},
		{name: "incomplete_usage", usage: `{"five_hour":{"utilization":5}}`, wantBlocked: true},
		{name: "probe_rejected", usage: `{"five_hour":{"utilization":5},"seven_day":{"utilization":93}}`, inferenceFails: true, wantProbe: true, wantBlocked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := &claudeQuotaRecheckExecutor{issue7737VerifyExecutor: &issue7737VerifyExecutor{id: "claude"}, t: t, usage: tc.usage}
			m := claudeQuotaTestManager(t, executor)
			m.recheckClaudeCredentialQuotas(context.Background())
			if executor.usageCalls != 0 {
				t.Fatal("healthy credential was polled")
			}
			markClaudeWeeklyQuota(m, t.Name())
			if tc.inferenceFails {
				executor.onStream = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					return nil, &Error{Code: ErrorCodeCredentialQuota, HTTPStatus: http.StatusTooManyRequests}
				}
			}
			m.recheckClaudeCredentialQuotas(context.Background())
			if executor.usageCalls != 1 || (executor.streamCalls.Load() != 0) != tc.wantProbe {
				t.Fatalf("got %d usage calls and %d inference calls", executor.usageCalls, executor.streamCalls.Load())
			}
			stored, _ := m.GetByID(t.Name())
			if blocked := stored.RecoveryStageAt(time.Now()) == RecoveryStageQuotaBlocked; blocked != tc.wantBlocked {
				t.Fatalf("quota blocked = %v, want %v", blocked, tc.wantBlocked)
			}
		})
	}
}
