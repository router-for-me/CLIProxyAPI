package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func isQuotaExhaustedEvidence(err error) bool {
	quotaErr, ok := errors.AsType[cliproxyexecutor.QuotaExhaustedError](err)
	return ok && quotaErr.IsQuotaExhausted()
}

func TestStatusErrorsReportOnlyStructuredQuotaExhaustion(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "codex usage_limit_reached",
			err:  newCodexStatusErr(http.StatusTooManyRequests, []byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_in_seconds":3600}}`)),
			want: true,
		},
		{
			name: "codex generic rate limit",
			err:  newCodexStatusErr(http.StatusTooManyRequests, []byte(`{"error":{"type":"rate_limit_exceeded","message":"Rate limit reached"}}`)),
			want: false,
		},
		{
			name: "codex websocket usage_limit_reached",
			err: func() error {
				err, _ := parseCodexWebsocketError([]byte(`{"type":"error","status":429,"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`))
				return err
			}(),
			want: true,
		},
		{
			name: "codex websocket usage_limit_reached with inferred status",
			err: func() error {
				err, _ := parseCodexWebsocketError([]byte(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_in_seconds":3600},"headers":{"retry-after":"11"}}`))
				return err
			}(),
			want: true,
		},
		{
			name: "codex websocket rate limit with inferred status and Retry-After",
			err: func() error {
				err, _ := parseCodexWebsocketError([]byte(`{"error":{"type":"rate_limit_error","message":"Too many requests"},"headers":{"retry-after":"7200"}}`))
				return err
			}(),
			want: false,
		},
		{
			name: "codex websocket overload with nested Retry-After",
			err: func() error {
				err, _ := parseCodexWebsocketError([]byte(`{"type":"error","status":503,"error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded.","headers":{"retry-after":"7200"}}}`))
				return err
			}(),
			want: false,
		},
		{
			name: "codex overload with Retry-After header",
			err:  newCodexStatusErrWithHeadersAndCooling(http.StatusServiceUnavailable, http.Header{"Retry-After": {"7200"}}, []byte(`{"error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded."}}`), false),
			want: false,
		},
		{
			name: "antigravity QUOTA_EXHAUSTED",
			err:  newAntigravityStatusErr(http.StatusTooManyRequests, []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Quota exhausted","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"}]}}`)),
			want: true,
		},
		{
			name: "antigravity long RATE_LIMIT_EXCEEDED",
			err:  newAntigravityStatusErr(http.StatusTooManyRequests, []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Rate limited","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"7200s"}]}}`)),
			want: false,
		},
		{
			name: "antigravity quota keyword only",
			err:  newAntigravityStatusErr(http.StatusTooManyRequests, []byte(`{"error":{"code":429,"message":"You have exhausted your capacity on this model. Your quota will reset after 2h."}}`)),
			want: false,
		},
		{
			name: "openai insufficient_quota",
			err:  newOpenAICompatStatusError(http.StatusTooManyRequests, nil, []byte(`{"error":{"message":"You exceeded your current quota","type":"insufficient_quota","code":"insufficient_quota"}}`)),
			want: true,
		},
		{
			name: "openai rate_limit_exceeded",
			err:  newOpenAICompatStatusError(http.StatusTooManyRequests, nil, []byte(`{"error":{"message":"Rate limit reached","type":"requests","code":"rate_limit_exceeded"}}`)),
			want: false,
		},
		{
			name: "openai insufficient_quota on a non-429",
			err:  newOpenAICompatStatusError(http.StatusForbidden, nil, []byte(`{"error":{"type":"insufficient_quota","code":"insufficient_quota"}}`)),
			want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.err == nil {
				t.Fatal("constructor returned nil error")
			}
			if got := isQuotaExhaustedEvidence(test.err); got != test.want {
				t.Fatalf("IsQuotaExhausted() = %v, want %v; err=%v", got, test.want, test.err)
			}
		})
	}
}

// A long upstream Retry-After hint is not quota evidence: the pool cooldown it
// causes stays a retryable answer with Retry-After, however far the reset is.
func TestCodexRetryAfterHintsWithoutQuotaCodeKeepPoolCooldownRetryable(t *testing.T) {
	websocketErr, ok := parseCodexWebsocketError([]byte(`{"error":{"type":"rate_limit_error","message":"Too many requests"},"headers":{"retry-after":"7200"}}`))
	if !ok {
		t.Fatal("websocket rate limit error was not recognized")
	}
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "http rate limit", err: newCodexStatusErrWithHeadersAndCooling(http.StatusTooManyRequests, http.Header{"Retry-After": {"7200"}}, []byte(`{"error":{"type":"rate_limit_exceeded","message":"Rate limit reached"}}`), false)},
		{name: "websocket rate limit with inferred status", err: websocketErr},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			hinted, ok := errors.AsType[interface {
				error
				RetryAfter() *time.Duration
			}](test.err)
			if !ok || hinted.RetryAfter() == nil || *hinted.RetryAfter() != 2*time.Hour {
				t.Fatalf("Codex constructor lost the upstream Retry-After hint: %v", test.err)
			}
			recorded := cliproxyauth.ResultErrorFromError(test.err)
			if isQuotaExhaustedEvidence(recorded) {
				t.Fatalf("a Retry-After hint was recorded as quota exhaustion: %+v", recorded)
			}
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.SetConfig(&config.Config{})
			manager.RegisterExecutor(NewCodexExecutor(&config.Config{}))
			const model = "quota-evidence-codex-retry-after"
			auth := &cliproxyauth.Auth{
				ID: t.Name(), Provider: "codex", Status: cliproxyauth.StatusActive,
				Attributes:  map[string]string{"api_key": "test"},
				ModelStates: map[string]*cliproxyauth.ModelState{model: {Status: cliproxyauth.StatusActive}},
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			if _, err := manager.Register(ctx, auth); err != nil {
				t.Fatal(err)
			}
			manager.MarkResult(ctx, cliproxyauth.Result{
				AuthID: auth.ID, Provider: auth.Provider, Model: model,
				Error: recorded, RetryAfter: hinted.RetryAfter(),
			})
			_, err := manager.SelectAuth(ctx, auth.Provider, model, cliproxyexecutor.Options{})
			if err == nil {
				t.Fatal("selection succeeded on a cooling credential")
			}
			if cliproxyauth.IsUsageLimitReachedError(err) {
				t.Fatalf("selection error = %v, want a retryable cooldown", err)
			}
			headers := cliproxyauth.SafeResponseHeaders(err)
			if headers.Get("X-Should-Retry") != "" {
				t.Fatalf("X-Should-Retry = %q, want absent without quota evidence", headers.Get("X-Should-Retry"))
			}
			if seconds, errParse := strconv.Atoi(headers.Get("Retry-After")); errParse != nil || seconds < 7100 || seconds > 7200 {
				t.Fatalf("Retry-After = %q, want the ~7200s upstream hint", headers.Get("Retry-After"))
			}
		})
	}
}

func TestCodexQuotaErrorsPreserveCredentialExhaustionThroughManager(t *testing.T) {
	body := []byte(`{"error":{"type":"usage_limit_reached","message":"Usage limit reached","resets_in_seconds":3600}}`)
	websocketErr, ok := parseCodexWebsocketError([]byte(`{"type":"error","status":429,"error":{"type":"usage_limit_reached","message":"Usage limit reached","resets_in_seconds":3600}}`))
	if !ok {
		t.Fatal("websocket quota error was not recognized")
	}
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "http", err: newCodexStatusErr(http.StatusTooManyRequests, body)},
		{name: "websocket", err: websocketErr},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			wrapped := fmt.Errorf("wrapped: %w", test.err)
			scoped, ok := errors.AsType[interface {
				error
				IsCredentialScoped() bool
				RetryAfter() *time.Duration
			}](wrapped)
			if !ok || !scoped.IsCredentialScoped() || scoped.RetryAfter() == nil {
				t.Fatalf("real Codex constructor lost credential scope or retry hint: %v", wrapped)
			}
			recorded := cliproxyauth.ResultErrorFromError(wrapped)
			if recorded.Code != cliproxyauth.ErrorCodeCredentialQuota || !isQuotaExhaustedEvidence(recorded) {
				t.Fatalf("conversion lost credential quota or exhaustion: %+v", recorded)
			}
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.SetConfig(&config.Config{})
			manager.RegisterExecutor(NewCodexExecutor(&config.Config{}))
			const reportedModel, siblingModel = "quota-evidence-codex-reported", "quota-evidence-codex-sibling"
			auth := &cliproxyauth.Auth{
				ID: t.Name(), Provider: "codex", Status: cliproxyauth.StatusActive,
				Attributes: map[string]string{"api_key": "test"},
				ModelStates: map[string]*cliproxyauth.ModelState{
					reportedModel: {Status: cliproxyauth.StatusActive},
					siblingModel:  {Status: cliproxyauth.StatusActive},
				},
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: reportedModel}, {ID: siblingModel}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			if _, err := manager.Register(ctx, auth); err != nil {
				t.Fatal(err)
			}
			manager.MarkResult(ctx, cliproxyauth.Result{
				AuthID: auth.ID, Provider: auth.Provider, Model: reportedModel,
				Error: recorded, RetryAfter: scoped.RetryAfter(), CredentialScope: scoped.IsCredentialScoped(),
			})
			current, ok := manager.GetByID(auth.ID)
			if !ok || !current.Quota.Exceeded || !current.Quota.Exhausted || current.Quota.Reason != "credential_quota" {
				t.Fatalf("manager lost credential-wide exhaustion: %+v", current)
			}
			for _, model := range []string{reportedModel, siblingModel} {
				state := current.ModelStates[model]
				if state == nil || !state.Quota.Exhausted {
					t.Fatalf("model %s lost propagated exhaustion: %+v", model, state)
				}
				_, err := manager.SelectAuth(ctx, auth.Provider, model, cliproxyexecutor.Options{})
				if !cliproxyauth.IsUsageLimitReachedError(err) {
					t.Fatalf("model %s selection error = %v, want final usage limit", model, err)
				}
				headers := cliproxyauth.SafeResponseHeaders(err)
				if headers.Get("Retry-After") != "" || headers.Get("X-Should-Retry") != "false" {
					t.Fatalf("model %s retry headers = %v, want final quota guidance", model, headers)
				}
			}
		})
	}
}
