package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type claudeThreadStatusErr struct {
	status int
	msg    string
	body   []byte
	header http.Header
}

func (e claudeThreadStatusErr) Error() string        { return e.msg }
func (e claudeThreadStatusErr) StatusCode() int      { return e.status }
func (e claudeThreadStatusErr) ResponseBody() []byte { return e.body }
func (e claudeThreadStatusErr) Headers() http.Header { return e.header }

type claudeThreadTransientRateLimitErr struct {
	claudeThreadStatusErr
	retryAfter time.Duration
}

func (claudeThreadTransientRateLimitErr) IsTransientRateLimit() bool { return true }

func (e claudeThreadTransientRateLimitErr) RetryAfter() *time.Duration {
	if e.retryAfter <= 0 {
		return nil
	}
	v := e.retryAfter
	return &v
}

type claudeThreadTestExecutor struct {
	executeFunc func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	streamFunc  func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}

func (e *claudeThreadTestExecutor) Identifier() string { return "claude" }

func (e *claudeThreadTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.executeFunc(ctx, auth, req, opts)
}

func (e *claudeThreadTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return e.streamFunc(ctx, auth, req, opts)
}

func (e *claudeThreadTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *claudeThreadTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *claudeThreadTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func registerClaudeThreadTestAuths(t *testing.T, manager *Manager, prefix string, model string) (owner *Auth, other *Auth) {
	t.Helper()
	owner = &Auth{
		ID:         prefix + "-owner",
		Provider:   "claude",
		Status:     StatusActive,
		Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
	}
	other = &Auth{
		ID:         prefix + "-other",
		Provider:   "claude",
		Status:     StatusActive,
		Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
	}
	for _, a := range []*Auth{owner, other} {
		registry.GetGlobalRegistry().RegisterClient(a.ID, "claude", []*registry.ModelInfo{{ID: model}})
		id := a.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		if _, errReg := manager.Register(WithSkipPersist(t.Context()), a); errReg != nil {
			t.Fatal(errReg)
		}
	}
	return owner, other
}

func executeStreamToResult(ctx context.Context, manager *Manager, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
	result, errStream := manager.ExecuteStream(ctx, []string{"claude"}, req, opts)
	if errStream != nil {
		return errStream
	}
	if result == nil {
		return errors.New("nil stream result")
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			return chunk.Err
		}
	}
	return nil
}

func TestClaudeThreadOwnerUnavailableReturnsThreadNotFound(t *testing.T) {
	const model = "claude-opus-5-5"

	cases := []struct {
		name             string
		stream           bool
		firstChunkStream bool
		disableOwner     bool
		cooldownOwner    bool
		ownerErr         error
		wantStatus       int
		wantNotFound     bool
		wantRetryAfter   string
		requestScopedCfg *internalconfig.Config
	}{
		{
			name:         "execute/disabled_owner",
			disableOwner: true,
			wantStatus:   http.StatusNotFound,
			wantNotFound: true,
		},
		{
			name:          "execute/cooling_down_owner",
			cooldownOwner: true,
			wantStatus:    http.StatusNotFound,
			wantNotFound:  true,
		},
		{
			name:         "execute/upstream_429",
			ownerErr:     claudeThreadStatusErr{status: http.StatusTooManyRequests, msg: "rate_limit_error"},
			wantStatus:   http.StatusNotFound,
			wantNotFound: true,
		},
		{
			name:         "execute/upstream_401",
			ownerErr:     claudeThreadStatusErr{status: http.StatusUnauthorized, msg: "authentication_error", body: []byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid token"}}`)},
			wantStatus:   http.StatusNotFound,
			wantNotFound: true,
		},
		{
			name:         "execute/upstream_403",
			ownerErr:     claudeThreadStatusErr{status: http.StatusForbidden, msg: "permission_error"},
			wantStatus:   http.StatusNotFound,
			wantNotFound: true,
		},
		{
			name:         "execute/upstream_402",
			ownerErr:     claudeThreadStatusErr{status: http.StatusPaymentRequired, msg: "payment_required"},
			wantStatus:   http.StatusNotFound,
			wantNotFound: true,
		},
		{
			name:         "execute/upstream_529_preserved",
			ownerErr:     claudeThreadStatusErr{status: 529, msg: "overloaded_error"},
			wantStatus:   529,
			wantNotFound: false,
		},
		{
			name:         "execute/upstream_500_preserved",
			ownerErr:     claudeThreadStatusErr{status: http.StatusInternalServerError, msg: "internal_server_error"},
			wantStatus:   http.StatusInternalServerError,
			wantNotFound: false,
		},
		{
			name:         "execute/request_fault_400_preserved",
			ownerErr:     claudeThreadStatusErr{status: http.StatusBadRequest, msg: "invalid_request_error", body: []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad input"}}`)},
			wantStatus:   http.StatusBadRequest,
			wantNotFound: false,
		},
		{
			name:           "execute/transient_rate_limit_preserved",
			ownerErr:       claudeThreadTransientRateLimitErr{claudeThreadStatusErr: claudeThreadStatusErr{status: http.StatusTooManyRequests, msg: "turn_rate_limit_exceeded", header: http.Header{"Retry-After": []string{"5"}}}, retryAfter: 5 * time.Second},
			wantStatus:     http.StatusTooManyRequests,
			wantNotFound:   false,
			wantRetryAfter: "5",
		},
		{
			name:           "execute/home_concurrency_busy_preserved",
			ownerErr:       NewHomeConcurrencyBusyError("busy", 12*time.Second),
			wantStatus:     http.StatusTooManyRequests,
			wantNotFound:   false,
			wantRetryAfter: "12",
		},
		{
			name:         "execute/credit_balance_too_low",
			ownerErr:     claudeThreadStatusErr{status: http.StatusBadRequest, msg: "invalid_request_error", body: []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."}}`)},
			wantStatus:   http.StatusNotFound,
			wantNotFound: true,
		},
		{
			name:         "execute/invalid_grant",
			ownerErr:     claudeThreadStatusErr{status: http.StatusBadRequest, msg: "invalid_grant: token revoked"},
			wantStatus:   http.StatusNotFound,
			wantNotFound: true,
		},
		{
			name:         "execute/credential_quota",
			ownerErr:     &Error{Code: ErrorCodeCredentialQuota, Message: "quota exhausted", HTTPStatus: http.StatusServiceUnavailable},
			wantStatus:   http.StatusNotFound,
			wantNotFound: true,
		},
		{
			name:         "execute/configured_stop_rule_preserved",
			ownerErr:     claudeThreadStatusErr{status: http.StatusForbidden, msg: "custom_stop_trigger"},
			wantStatus:   http.StatusForbidden,
			wantNotFound: false,
			requestScopedCfg: &internalconfig.Config{
				OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
					"claude": {
						{
							Action: RequestScopedActionStop,
							Status: http.StatusForbidden,
							Match:  []string{"custom_stop_trigger"},
						},
					},
				},
			},
		},
		{
			name:         "stream/disabled_owner",
			stream:       true,
			disableOwner: true,
			wantStatus:   http.StatusNotFound,
			wantNotFound: true,
		},
		{
			name:          "stream/cooling_down_owner",
			stream:        true,
			cooldownOwner: true,
			wantStatus:    http.StatusNotFound,
			wantNotFound:  true,
		},
		{
			name:         "stream/status_429",
			stream:       true,
			ownerErr:     claudeThreadStatusErr{status: http.StatusTooManyRequests, msg: "rate_limit_error"},
			wantStatus:   http.StatusNotFound,
			wantNotFound: true,
		},
		{
			name:             "stream/first_chunk_401",
			stream:           true,
			firstChunkStream: true,
			ownerErr:         claudeThreadStatusErr{status: http.StatusUnauthorized, msg: "authentication_error", body: []byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid token"}}`)},
			wantStatus:       http.StatusNotFound,
			wantNotFound:     true,
		},
		{
			name:             "stream/first_chunk_529_preserved",
			stream:           true,
			firstChunkStream: true,
			ownerErr:         claudeThreadStatusErr{status: 529, msg: "overloaded_error"},
			wantStatus:       529,
			wantNotFound:     false,
		},
		{
			name:         "stream/configured_stop_rule_preserved",
			stream:       true,
			ownerErr:     claudeThreadStatusErr{status: http.StatusTooManyRequests, msg: "custom_stop_stream"},
			wantStatus:   http.StatusTooManyRequests,
			wantNotFound: false,
			requestScopedCfg: &internalconfig.Config{
				OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
					"claude": {
						{
							Action: RequestScopedActionStop,
							Status: http.StatusTooManyRequests,
							Match:  []string{"custom_stop_stream"},
						},
					},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caller := t.Name() + "-" + uuid.NewString()
			msgID := "msg_thread_" + uuid.NewString()

			manager := NewManager(nil, &RoundRobinSelector{}, nil)
			manager.SetRetryConfig(0, 0, 0)
			owner, other := registerClaudeThreadTestAuths(t, manager, caller, model)

			var calledAuths []string
			exec := &claudeThreadTestExecutor{
				executeFunc: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					calledAuths = append(calledAuths, auth.ID)
					if tc.ownerErr != nil && auth.ID == owner.ID {
						return cliproxyexecutor.Response{}, tc.ownerErr
					}
					return cliproxyexecutor.Response{Payload: []byte(`{"id":"msg_ok"}`)}, nil
				},
				streamFunc: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					calledAuths = append(calledAuths, auth.ID)
					if tc.ownerErr != nil && auth.ID == owner.ID {
						if tc.firstChunkStream {
							ch := make(chan cliproxyexecutor.StreamChunk, 1)
							ch <- cliproxyexecutor.StreamChunk{Err: tc.ownerErr}
							close(ch)
							return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
						}
						return nil, tc.ownerErr
					}
					ch := make(chan cliproxyexecutor.StreamChunk, 1)
					ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"message_stop"}` + "\n\n")}
					close(ch)
					return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
				},
			}
			manager.RegisterExecutor(exec)

			if tc.disableOwner {
				ownerCopy := owner.Clone()
				ownerCopy.Disabled = true
				if _, errUpd := manager.Update(WithSkipPersist(t.Context()), ownerCopy); errUpd != nil {
					t.Fatal(errUpd)
				}
			}
			if tc.cooldownOwner {
				manager.MarkResult(WithSkipPersist(t.Context()), Result{
					AuthID:     owner.ID,
					Provider:   "claude",
					Model:      model,
					RouteModel: model,
					Success:    false,
					Error:      &Error{Code: "rate_limit", Message: "cooling down", HTTPStatus: http.StatusTooManyRequests, Retryable: true},
				})
			}

			opts := cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatClaude,
				Stream:       tc.stream,
				Metadata: map[string]any{
					cliproxyexecutor.CallerScopeMetadataKey: caller,
				},
			}
			ownerScope := cliproxysession.ClaudeThreadCallerScope("", opts.Metadata)
			cliproxysession.RememberClaudeThreadAuth(ownerScope, msgID, owner.ID)

			req := cliproxyexecutor.Request{
				Model:   model,
				Payload: []byte(`{"model":"` + model + `","thread":{"type":"continue","previous_message_id":"` + msgID + `"},"messages":[{"role":"user","content":"next"}]}`),
			}

			ctx := WithSkipPersist(t.Context())
			if tc.requestScopedCfg != nil {
				ctx = WithRequestScopedErrorConfig(ctx, tc.requestScopedCfg)
			}

			var errExec error
			if tc.stream {
				errExec = executeStreamToResult(ctx, manager, req, opts)
			} else {
				_, errExec = manager.Execute(ctx, []string{"claude"}, req, opts)
			}
			if errExec == nil {
				t.Fatal("expected error, got nil")
			}

			for _, id := range calledAuths {
				if id == other.ID {
					t.Fatalf("continuation leaked to non-owning credential %q (calls=%v)", other.ID, calledAuths)
				}
			}

			if gotStatus := statusCodeFromError(errExec); gotStatus != tc.wantStatus {
				t.Fatalf("status = %d, want %d (err=%v)", gotStatus, tc.wantStatus, errExec)
			}
			if gotNotFound := clienterror.IsClaudeThreadNotFound(statusCodeFromError(errExec), errExec); gotNotFound != tc.wantNotFound {
				t.Fatalf("IsClaudeThreadNotFound() = %v, want %v (err=%v)", gotNotFound, tc.wantNotFound, errExec)
			}
			if tc.wantRetryAfter != "" {
				if got := SafeResponseHeaders(errExec).Get("Retry-After"); got != tc.wantRetryAfter {
					t.Fatalf("Retry-After = %q, want %q", got, tc.wantRetryAfter)
				}
			}
		})
	}
}

func TestClaudeThreadCreateStillRotatesPastRateLimitedCredential(t *testing.T) {
	const model = "claude-opus-5-5"
	caller := t.Name() + "-" + uuid.NewString()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	first, second := registerClaudeThreadTestAuths(t, manager, caller, model)

	var calledAuths []string
	manager.RegisterExecutor(&claudeThreadTestExecutor{
		executeFunc: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			calledAuths = append(calledAuths, auth.ID)
			if len(calledAuths) == 1 {
				return cliproxyexecutor.Response{}, claudeThreadStatusErr{status: http.StatusTooManyRequests, msg: "rate_limit_error"}
			}
			return cliproxyexecutor.Response{Payload: []byte(`{"id":"msg_created"}`)}, nil
		},
		streamFunc: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			return nil, errors.New("unexpected stream call")
		},
	})

	_ = first
	_ = second
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey: caller,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(`{"model":"` + model + `","thread":{"type":"create"},"messages":[{"role":"user","content":"hello"}]}`),
	}

	resp, errExec := manager.Execute(WithSkipPersist(t.Context()), []string{"claude"}, req, opts)
	if errExec != nil {
		t.Fatalf("thread create should rotate past rate-limited credential, got err=%v", errExec)
	}
	if string(resp.Payload) != `{"id":"msg_created"}` || len(calledAuths) != 2 {
		t.Fatalf("unexpected rotation outcome: payload=%s called=%v", resp.Payload, calledAuths)
	}
}

func TestClaudeThreadHomeModeDispatchErrors(t *testing.T) {
	const model = "claude-opus-5-5"

	cases := []struct {
		name             string
		stream           bool
		homePayload      []byte
		execErr          error
		wantStatus       int
		wantNotFound     bool
		wantCode         string
		requestScopedCfg *internalconfig.Config
	}{
		{
			name:         "execute/user_credits_insufficient_preserved",
			homePayload:  []byte(`{"error":{"type":"user_credits_insufficient","message":"insufficient user credits"}}`),
			wantStatus:   http.StatusPaymentRequired,
			wantNotFound: false,
			wantCode:     "user_credits_insufficient",
		},
		{
			name:         "stream/user_credits_insufficient_preserved",
			stream:       true,
			homePayload:  []byte(`{"error":{"type":"user_credits_insufficient","message":"insufficient user credits"}}`),
			wantStatus:   http.StatusPaymentRequired,
			wantNotFound: false,
			wantCode:     "user_credits_insufficient",
		},
		{
			name:         "execute/user_period_limit_exceeded_preserved",
			homePayload:  []byte(`{"error":{"type":"user_period_limit_exceeded","message":"user period limit reached"}}`),
			wantStatus:   http.StatusTooManyRequests,
			wantNotFound: false,
			wantCode:     "user_period_limit_exceeded",
		},
		{
			name:         "stream/user_period_limit_exceeded_preserved",
			stream:       true,
			homePayload:  []byte(`{"error":{"type":"user_period_limit_exceeded","message":"user period limit reached"}}`),
			wantStatus:   http.StatusTooManyRequests,
			wantNotFound: false,
			wantCode:     "user_period_limit_exceeded",
		},
		{
			name:         "execute/home_concurrency_busy_preserved",
			homePayload:  []byte(`{"error":{"type":"credential_concurrency_exceeded","message":"busy","retryable":true,"retry_after_ms":15000}}`),
			wantStatus:   http.StatusTooManyRequests,
			wantNotFound: false,
		},
		{
			name:         "stream/home_concurrency_busy_preserved",
			stream:       true,
			homePayload:  []byte(`{"error":{"type":"credential_concurrency_exceeded","message":"busy","retryable":true,"retry_after_ms":15000}}`),
			wantStatus:   http.StatusTooManyRequests,
			wantNotFound: false,
		},
		{
			name:         "execute/home_auth_not_found_rewritten",
			homePayload:  []byte(`{"error":{"type":"auth_not_found","message":"no auth available"}}`),
			wantStatus:   http.StatusNotFound,
			wantNotFound: true,
			wantCode:     "thread_not_found",
		},
		{
			name:         "stream/home_auth_not_found_rewritten",
			stream:       true,
			homePayload:  []byte(`{"error":{"type":"auth_not_found","message":"no auth available"}}`),
			wantStatus:   http.StatusNotFound,
			wantNotFound: true,
			wantCode:     "thread_not_found",
		},
		{
			name:         "execute/home_model_cooldown_rewritten",
			homePayload:  []byte(`{"error":{"type":"model_cooldown","message":"credential is cooling down","retryable":true,"retry_after_ms":5000}}`),
			wantStatus:   http.StatusNotFound,
			wantNotFound: true,
			wantCode:     "thread_not_found",
		},
		{
			name:         "stream/home_model_cooldown_rewritten",
			stream:       true,
			homePayload:  []byte(`{"error":{"type":"model_cooldown","message":"credential is cooling down","retryable":true,"retry_after_ms":5000}}`),
			wantStatus:   http.StatusNotFound,
			wantNotFound: true,
			wantCode:     "thread_not_found",
		},
		{
			name:         "execute/home_configured_stop_rule_preserved",
			execErr:      claudeThreadStatusErr{status: http.StatusForbidden, msg: "home_stop_trigger"},
			wantStatus:   http.StatusForbidden,
			wantNotFound: false,
			requestScopedCfg: &internalconfig.Config{
				Home: internalconfig.HomeConfig{Enabled: true},
				OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
					"claude": {
						{
							Action: RequestScopedActionStop,
							Status: http.StatusForbidden,
							Match:  []string{"home_stop_trigger"},
						},
					},
				},
			},
		},
		{
			name:         "stream/home_configured_stop_rule_preserved",
			stream:       true,
			execErr:      claudeThreadStatusErr{status: http.StatusForbidden, msg: "home_stop_trigger"},
			wantStatus:   http.StatusForbidden,
			wantNotFound: false,
			requestScopedCfg: &internalconfig.Config{
				Home: internalconfig.HomeConfig{Enabled: true},
				OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
					"claude": {
						{
							Action: RequestScopedActionStop,
							Status: http.StatusForbidden,
							Match:  []string{"home_stop_trigger"},
						},
					},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caller := t.Name() + "-" + uuid.NewString()
			msgID := "msg_thread_" + uuid.NewString()
			ownerID := caller + "-owner"

			homePayload := tc.homePayload
			if len(homePayload) == 0 {
				homePayload = []byte(`{"auth_index":"` + ownerID + `","auth":{"id":"` + ownerID + `","provider":"claude","attributes":{"auth_kind":"oauth"}}}`)
			}

			dispatcher := &fixtureHomeDispatcher{payload: homePayload}
			manager := NewManager(nil, nil, nil)
			cfg := tc.requestScopedCfg
			if cfg == nil {
				cfg = &internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}}
			}
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 0)
			manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)

			owner := &Auth{
				ID:         ownerID,
				Provider:   "claude",
				Status:     StatusActive,
				Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
			}
			registry.GetGlobalRegistry().RegisterClient(owner.ID, "claude", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(owner.ID) })
			if _, errReg := manager.Register(WithSkipPersist(t.Context()), owner); errReg != nil {
				t.Fatal(errReg)
			}

			manager.RegisterExecutor(&claudeThreadTestExecutor{
				executeFunc: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					if tc.execErr != nil {
						return cliproxyexecutor.Response{}, tc.execErr
					}
					return cliproxyexecutor.Response{Payload: []byte(`{"id":"msg_ok"}`)}, nil
				},
				streamFunc: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					if tc.execErr != nil {
						return nil, tc.execErr
					}
					ch := make(chan cliproxyexecutor.StreamChunk, 1)
					ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"message_stop"}` + "\n\n")}
					close(ch)
					return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
				},
			})

			opts := cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatClaude,
				Stream:       tc.stream,
				Metadata: map[string]any{
					cliproxyexecutor.CallerScopeMetadataKey: caller,
				},
			}
			ownerScope := cliproxysession.ClaudeThreadCallerScope("", opts.Metadata)
			cliproxysession.RememberClaudeThreadAuth(ownerScope, msgID, owner.ID)

			req := cliproxyexecutor.Request{
				Model:   model,
				Payload: []byte(`{"model":"` + model + `","thread":{"type":"continue","previous_message_id":"` + msgID + `"},"messages":[{"role":"user","content":"next"}]}`),
			}

			ctx := WithRequestScopedErrorConfig(WithSkipPersist(t.Context()), cfg)

			var errExec error
			if tc.stream {
				errExec = executeStreamToResult(ctx, manager, req, opts)
			} else {
				_, errExec = manager.Execute(ctx, []string{"claude"}, req, opts)
			}
			if errExec == nil {
				t.Fatal("expected error, got nil")
			}

			if gotStatus := statusCodeFromError(errExec); gotStatus != tc.wantStatus {
				t.Fatalf("status = %d, want %d (err=%v)", gotStatus, tc.wantStatus, errExec)
			}
			if gotNotFound := clienterror.IsClaudeThreadNotFound(statusCodeFromError(errExec), errExec); gotNotFound != tc.wantNotFound {
				t.Fatalf("IsClaudeThreadNotFound() = %v, want %v (err=%v)", gotNotFound, tc.wantNotFound, errExec)
			}
			if tc.wantCode != "" {
				if gotCode := errorCodeFromError(errExec); gotCode != tc.wantCode {
					t.Fatalf("error code = %q, want %q (err=%v)", gotCode, tc.wantCode, errExec)
				}
			}
		})
	}
}

func TestClaudeThreadHomeRepeatedCooldownReturnsThreadNotFound(t *testing.T) {
	const model = "claude-opus-5-5"

	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "execute", true: "stream"}[stream], func(t *testing.T) {
			caller := t.Name() + "-" + uuid.NewString()
			msgID := "msg_thread_" + uuid.NewString()
			ownerID := caller + "-owner"

			dispatcher := &fixtureHomeDispatcher{
				payloads: [][]byte{
					[]byte(`{"auth_index":"` + ownerID + `","auth":{"id":"` + ownerID + `","provider":"claude","attributes":{"auth_kind":"oauth"}}}`),
					[]byte(`{"error":{"type":"model_cooldown","message":"credential is cooling down","retryable":true,"retry_after_ms":1,"request_retry":1}}`),
					[]byte(`{"error":{"type":"model_cooldown","message":"credential is cooling down","retryable":true,"retry_after_ms":1,"request_retry":1}}`),
				},
			}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.SetRetryConfig(1, time.Second, 1)
			manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)

			owner := &Auth{
				ID:         ownerID,
				Provider:   "claude",
				Status:     StatusActive,
				Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
			}
			registry.GetGlobalRegistry().RegisterClient(owner.ID, "claude", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(owner.ID) })
			if _, errReg := manager.Register(WithSkipPersist(t.Context()), owner); errReg != nil {
				t.Fatal(errReg)
			}

			rateLimitErr := claudeThreadTransientRateLimitErr{
				claudeThreadStatusErr: claudeThreadStatusErr{
					status: http.StatusTooManyRequests,
					msg:    "rate_limit_error",
				},
				retryAfter: time.Millisecond,
			}
			manager.RegisterExecutor(&claudeThreadTestExecutor{
				executeFunc: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					return cliproxyexecutor.Response{}, rateLimitErr
				},
				streamFunc: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					return nil, rateLimitErr
				},
			})

			opts := cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatClaude,
				Stream:       stream,
				Metadata: map[string]any{
					cliproxyexecutor.CallerScopeMetadataKey: caller,
				},
			}
			ownerScope := cliproxysession.ClaudeThreadCallerScope("", opts.Metadata)
			cliproxysession.RememberClaudeThreadAuth(ownerScope, msgID, owner.ID)

			req := cliproxyexecutor.Request{
				Model:   model,
				Payload: []byte(`{"model":"` + model + `","thread":{"type":"continue","previous_message_id":"` + msgID + `"},"messages":[{"role":"user","content":"next"}]}`),
			}

			ctx := WithSkipPersist(t.Context())
			var errExec error
			if stream {
				errExec = executeStreamToResult(ctx, manager, req, opts)
			} else {
				_, errExec = manager.Execute(ctx, []string{"claude"}, req, opts)
			}
			if errExec == nil {
				t.Fatal("expected error, got nil")
			}
			if gotStatus := statusCodeFromError(errExec); gotStatus != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (err=%v)", gotStatus, errExec)
			}
			if !clienterror.IsClaudeThreadNotFound(statusCodeFromError(errExec), errExec) {
				t.Fatalf("IsClaudeThreadNotFound() = false, want true (err=%v)", errExec)
			}
			if gotCode := errorCodeFromError(errExec); gotCode != "thread_not_found" {
				t.Fatalf("error code = %q, want thread_not_found (err=%v)", gotCode, errExec)
			}
		})
	}
}
