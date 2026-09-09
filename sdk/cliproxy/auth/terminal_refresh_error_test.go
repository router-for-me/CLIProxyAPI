package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func terminalRefreshTestAuth(id string, now time.Time) *Auth {
	return &Auth{
		ID: id, Provider: "codex", Status: StatusError, Unavailable: true,
		StatusMessage: "unauthorized",
		LastError:     &Error{Code: "unauthorized", Message: "refresh_token_reused", HTTPStatus: http.StatusUnauthorized},
		Metadata:      map[string]any{"access_token": "expired-fixture", "expired": now.Add(-time.Hour).Format(time.RFC3339)},
	}
}

func TestTerminalRefreshSelectionRequiresEveryCandidate(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name     string
		alter    func(*Auth)
		terminal bool
		ready    bool
	}{
		{name: "all terminal", terminal: true},
		{name: "refresh scheduled", alter: func(a *Auth) { a.NextRefreshAfter = now.Add(time.Minute) }},
		{name: "unauthorized request cooldown", alter: func(a *Auth) { a.NextRetryAfter = now.Add(30 * time.Minute) }},
		{name: "quota recovery scheduled", alter: func(a *Auth) { a.Quota.NextRecoverAt = now.Add(time.Hour) }},
		{name: "expired without refresh failure", alter: func(a *Auth) { a.LastError = nil }},
		{name: "transient refresh", alter: func(a *Auth) {
			a.LastError = &Error{HTTPStatus: http.StatusBadGateway, Message: "temporary failure", Retryable: true}
		}},
		{name: "retryable unauthorized", alter: func(a *Auth) { a.LastError.Retryable = true }},
		{name: "other unavailable state", alter: func(a *Auth) { a.StatusMessage = "operator hold" }},
		{name: "api key", alter: func(a *Auth) { a.Attributes = map[string]string{AttributeAuthKind: AuthKindAPIKey} }},
		{name: "unexpired but held", alter: func(a *Auth) { a.Metadata["expired"] = now.Add(time.Hour).Format(time.RFC3339) }},
		{name: "healthy", ready: true, alter: func(a *Auth) {
			a.Metadata["expired"] = now.Add(time.Hour).Format(time.RFC3339)
			a.Unavailable, a.Status, a.StatusMessage, a.LastError = false, StatusActive, "", nil
		}},
		{name: "quota cooldown", alter: func(a *Auth) {
			a.Metadata["expired"] = now.Add(time.Hour).Format(time.RFC3339)
			a.StatusMessage = "quota"
			a.LastError = &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}
			a.Quota = QuotaState{Exceeded: true, NextRecoverAt: now.Add(time.Hour)}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first := terminalRefreshTestAuth("terminal-first", now)
			second := terminalRefreshTestAuth("terminal-second", now)
			if test.alter != nil {
				test.alter(second)
			}
			auths := []*Auth{first, second}
			manager := NewManager(nil, nil, nil)
			t.Cleanup(manager.StopAutoRefresh)
			for _, pick := range []struct {
				name string
				run  func() ([]*Auth, error)
			}{
				{"selector", func() ([]*Auth, error) { return getAvailableAuths(auths, "codex", "fixture", now) }},
				{"manager", func() ([]*Auth, error) { return manager.availableAuthsForRouteModel(auths, "codex", "fixture", now) }},
			} {
				t.Run(pick.name, func(t *testing.T) {
					available, err := pick.run()
					if got := IsUpstreamAuthenticationRequired(err); got != test.terminal {
						t.Fatalf("terminal = %v, want %v; error = %v", got, test.terminal, err)
					}
					if (len(available) > 0) != test.ready {
						t.Fatalf("available = %v, want ready %v", available, test.ready)
					}
				})
			}
		})
	}
}

func TestTerminalRefreshSchedulerEligibility(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		for _, alternative := range []string{"terminal", "healthy", "transient", "disabled", "wrong model", "excluded by kind", "already tried", "not pinned"} {
			t.Run(fmt.Sprintf("mixed=%t/%s", mixed, alternative), func(t *testing.T) {
				ctx := context.Background()
				now := time.Now()
				model := "terminal-refresh-" + t.Name()
				first := terminalRefreshTestAuth(model+"-first", now)
				second := terminalRefreshTestAuth(model+"-second", now)
				if mixed {
					second.Provider = "claude"
				}
				terminal := true
				var tried map[string]struct{}
				opts := cliproxyexecutor.Options{}
				secondModel := model
				if alternative != "terminal" {
					second.Metadata["expired"] = now.Add(time.Hour).Format(time.RFC3339)
					second.Unavailable, second.Status, second.StatusMessage, second.LastError = false, StatusActive, "", nil
				}
				switch alternative {
				case "healthy":
					terminal = false
				case "transient":
					terminal = false
					second.Unavailable, second.Status = true, StatusError
					second.LastError = &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "temporary"}
				case "disabled":
					second.Disabled = true
				case "wrong model":
					secondModel += "-other"
				case "excluded by kind":
					second.Attributes = map[string]string{AttributeAuthKind: AuthKindAPIKey}
					ctx = withRequiredAuthKind(ctx, AuthKindOAuth)
				case "already tried":
					tried = map[string]struct{}{second.ID: {}}
				case "not pinned":
					opts.Metadata = map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: first.ID}
				}
				registerSchedulerModels(t, first.Provider, model, first.ID)
				registerSchedulerModels(t, second.Provider, secondModel, second.ID)
				scheduler := newAuthScheduler(&RoundRobinSelector{})
				scheduler.rebuild([]*Auth{first, second})
				var selected *Auth
				var err error
				if mixed {
					selected, _, err = scheduler.pickMixed(ctx, []string{"codex", "claude"}, model, opts, tried)
				} else {
					selected, err = scheduler.pickSingle(ctx, "codex", model, opts, tried)
				}
				if got := IsUpstreamAuthenticationRequired(err); got != terminal {
					t.Fatalf("terminal = %v, want %v; error = %v", got, terminal, err)
				}
				if alternative == "healthy" && (selected == nil || selected.ID != second.ID) {
					t.Fatalf("selected = %v, want healthy alternative", selected)
				}
			})
		}
	}
}

func TestTerminalRefreshErrorContract(t *testing.T) {
	cause := &Error{Code: "unauthorized", Message: "refresh_token_reused; access_token=secret-fixture"}
	err := newUpstreamAuthenticationRequiredError(cause)
	wrapped := fmt.Errorf("wrapped: %w", err)
	if !IsUpstreamAuthenticationRequired(wrapped) || !errors.Is(wrapped, cause) {
		t.Fatal("terminal classification or underlying cause lost through wrapping")
	}
	var selectionErr *Error
	if !errors.As(wrapped, &selectionErr) || selectionErr.Code != "auth_unavailable" || selectionErr.Retryable || selectionErr.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("selection compatibility = %#v", selectionErr)
	}
	if got := gjson.Get(err.Error(), "error.code").String(); got != ErrorCodeUpstreamAuthenticationRequired {
		t.Fatalf("client code = %q", got)
	}
	if strings.Contains(err.Error(), "secret-fixture") {
		t.Fatalf("upstream secret leaked: %s", err)
	}
	if !shouldRetrySchedulerPick(err) {
		t.Fatal("terminal snapshot must still allow rebuilding a stale scheduler")
	}
	manager := NewManager(nil, nil, nil)
	t.Cleanup(manager.StopAutoRefresh)
	manager.SetRetryConfig(3, time.Minute, 0)
	if wait, retry := manager.shouldRetryAfterError(wrapped, 0, []string{"codex"}, "fixture", time.Minute); retry || wait != 0 {
		t.Fatalf("terminal failure retried: wait=%s retry=%v", wait, retry)
	}
	if IsUpstreamAuthenticationRequired(WithCause(&Error{Code: "auth_unavailable"}, cause)) {
		t.Fatal("a last unauthorized cause alone does not classify the entire pool")
	}
}

func TestTerminalRefreshFailureAndRecoveryUseActualRefreshState(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	t.Cleanup(manager.StopAutoRefresh)
	manager.RegisterExecutor(unauthorizedRefreshTestExecutor{schedulerProviderTestExecutor{provider: "codex"}})
	const model = "terminal-refresh-real-transition"
	a := &Auth{ID: model, Provider: "codex", Metadata: map[string]any{"email": "fixture@example.test"}}
	registerSchedulerModels(t, "codex", model, a.ID)
	if _, errRegister := manager.Register(ctx, a); errRegister != nil {
		t.Fatal(errRegister)
	}
	manager.refreshAuth(ctx, a.ID)
	if _, _, err := manager.pickNext(ctx, "codex", model, cliproxyexecutor.Options{}, nil); !IsUpstreamAuthenticationRequired(err) {
		t.Fatalf("after unauthorized refresh: %v", err)
	}
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "codex"})
	manager.refreshAuth(ctx, a.ID)
	if selected, _, err := manager.pickNext(ctx, "codex", model, cliproxyexecutor.Options{}, nil); err != nil || selected == nil || selected.ID != a.ID {
		t.Fatalf("after successful refresh: selected=%v error=%v", selected, err)
	}
}
