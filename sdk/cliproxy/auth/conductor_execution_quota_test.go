package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type quotaAttemptIsolationSelector struct{}

func (quotaAttemptIsolationSelector) Pick(_ context.Context, _, _ string, _ cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	var selected *Auth
	for _, auth := range auths {
		if auth != nil && (selected == nil || auth.ID < selected.ID) {
			selected = auth
		}
	}
	return selected, nil
}

type quotaAttemptIsolationExecutor struct{}

func (*quotaAttemptIsolationExecutor) Identifier() string { return "codex" }

func (*quotaAttemptIsolationExecutor) ShouldPrepareRequestAuth(auth *Auth) bool {
	return auth != nil && strings.HasSuffix(auth.ID, "-b")
}

func (*quotaAttemptIsolationExecutor) PrepareRequestAuth(context.Context, *Auth) (*Auth, error) {
	return nil, &Error{HTTPStatus: http.StatusInternalServerError, Message: "prepare failed before upstream response"}
}

func (*quotaAttemptIsolationExecutor) Execute(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	setQuotaAttemptIsolationHeaders(ctx)
	return cliproxyexecutor.Response{}, quotaAttemptIsolationError()
}

func (*quotaAttemptIsolationExecutor) ExecuteStream(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	setQuotaAttemptIsolationHeaders(ctx)
	return nil, quotaAttemptIsolationError()
}

func (*quotaAttemptIsolationExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*quotaAttemptIsolationExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (*quotaAttemptIsolationExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func setQuotaAttemptIsolationHeaders(ctx context.Context) {
	internallogging.SetResponseHeaders(ctx, http.Header{
		"X-Codex-Plan-Type":                   []string{"pro"},
		"X-Codex-Primary-Used-Percent":        []string{"91"},
		"X-Codex-Primary-Window-Minutes":      []string{"10080"},
		"X-Codex-Primary-Reset-After-Seconds": []string{"3600"},
	})
}

func quotaAttemptIsolationError() error {
	return &Error{HTTPStatus: http.StatusInternalServerError, Message: "first upstream attempt failed"}
}

func TestExecutionAttemptsDoNotReuseQuotaResponseHeaders(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Manager, context.Context, string) error
	}{
		{
			name: "non-stream",
			run: func(manager *Manager, ctx context.Context, model string) error {
				_, errExecute := manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "stream",
			run: func(manager *Manager, ctx context.Context, model string) error {
				_, errExecute := manager.ExecuteStream(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
				return errExecute
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := NewManager(nil, quotaAttemptIsolationSelector{}, nil)
			manager.RegisterExecutor(&quotaAttemptIsolationExecutor{})
			model := "gpt-quota-attempt-isolation-" + test.name
			firstID := "quota-attempt-" + test.name + "-a"
			secondID := "quota-attempt-" + test.name + "-b"
			for _, id := range []string{firstID, secondID} {
				if _, errRegister := manager.Register(context.Background(), &Auth{
					ID:       id,
					Provider: "codex",
					Status:   StatusActive,
				}); errRegister != nil {
					t.Fatalf("Register(%s) error = %v", id, errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			}

			ctx := internallogging.WithResponseHeadersHolder(context.Background())
			if errExecute := test.run(manager, ctx, model); errExecute == nil {
				t.Fatal("execution error = nil, want terminal prepare error")
			}

			first, okFirst := manager.GetByID(firstID)
			second, okSecond := manager.GetByID(secondID)
			if !okFirst || first == nil || !okSecond || second == nil {
				t.Fatalf("auth lookup failed: first=%#v second=%#v", first, second)
			}
			if got := first.Quota.Signals["X-Codex-Primary-Used-Percent"]; got != "91" {
				t.Fatalf("first attempt observation = %q, want 91; quota=%#v", got, first.Quota)
			}
			if len(second.Quota.Signals) != 0 || !second.Quota.ObservedAt.IsZero() {
				t.Fatalf("pre-response failure inherited earlier attempt headers: %#v", second.Quota)
			}
			if headers := internallogging.GetResponseHeaders(ctx); len(headers) != 0 {
				t.Fatalf("attempt headers leaked into request holder: %#v", headers)
			}
		})
	}
}
