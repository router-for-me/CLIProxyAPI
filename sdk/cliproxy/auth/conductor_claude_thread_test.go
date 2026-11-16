package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// directStreamErrorExecutor fails before streaming, as the Claude executor
// does for a non-2xx upstream status.
type directStreamErrorExecutor struct {
	*authFallbackExecutor
}

func (e *directStreamErrorExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.streamCalls = append(e.streamCalls, auth.ID)
	err := e.streamFirstErrors[auth.ID]
	e.mu.Unlock()
	if err != nil {
		return nil, err
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

type claudeThreadOwnerTest struct {
	manager  *Manager
	executor *authFallbackExecutor
	ownerID  string
	otherID  string
	model    string
}

func newClaudeThreadOwnerTest(t *testing.T, ownerErr error, ownerDisabled bool) claudeThreadOwnerTest {
	t.Helper()
	m := NewManager(nil, nil, nil)
	tc := claudeThreadOwnerTest{
		manager: m,
		ownerID: t.Name() + "-owner",
		otherID: t.Name() + "-other",
		model:   "claude-thread-owner-model",
	}
	tc.executor = &authFallbackExecutor{
		id:                "claude",
		executeErrors:     map[string]error{tc.ownerID: ownerErr},
		streamFirstErrors: map[string]error{tc.ownerID: ownerErr},
	}
	m.RegisterExecutor(tc.executor)
	reg := registry.GetGlobalRegistry()
	for _, auth := range []*Auth{
		{ID: tc.ownerID, Provider: "claude", Disabled: ownerDisabled},
		{ID: tc.otherID, Provider: "claude"},
	} {
		reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: tc.model}})
		authID := auth.ID
		t.Cleanup(func() { reg.UnregisterClient(authID) })
		if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
	}
	return tc
}

// continuation binds a fresh previous_message_id to the owner, as the Claude
// executor does after the thread-creating response.
func (tc claudeThreadOwnerTest) continuation(t *testing.T) (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	t.Helper()
	messageID := "msg_" + t.Name()
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
	req := cliproxyexecutor.Request{
		Model:   tc.model,
		Payload: []byte(`{"model":"` + tc.model + `","thread":{"type":"continue","previous_message_id":"` + messageID + `"},"messages":[{"role":"user","content":"next"}]}`),
	}
	cliproxysession.RememberClaudeThreadAuth(cliproxysession.ClaudeThreadCallerScope("", opts.Metadata, req.Metadata), messageID, tc.ownerID)
	return req, opts
}

func assertClaudeThreadReplay(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected thread_not_found, got success")
	}
	if status := statusCodeFromError(err); status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; err=%v", status, err)
	}
	if !clienterror.IsClaudeThreadNotFound(http.StatusNotFound, err) {
		t.Fatalf("error is not recognized as a Claude missing-thread response: %v", err)
	}
}

func TestClaudeThreadContinuationReplaysWhenOwnerIsUnavailable(t *testing.T) {
	cases := []struct {
		name     string
		ownerErr error
		disabled bool
	}{
		{name: "rate limited", ownerErr: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate_limit_error: usage limit reached"}},
		{name: "unauthorized", ownerErr: &Error{HTTPStatus: http.StatusUnauthorized, Message: "authentication_error: invalid token"}},
		{name: "forbidden", ownerErr: &Error{HTTPStatus: http.StatusForbidden, Message: "permission_error: organization disabled"}},
		{name: "disabled", disabled: true},
	}
	for _, c := range cases {
		t.Run(c.name+"/execute", func(t *testing.T) {
			tc := newClaudeThreadOwnerTest(t, c.ownerErr, c.disabled)
			req, opts := tc.continuation(t)
			_, errExecute := tc.manager.Execute(context.Background(), []string{"claude"}, req, opts)
			assertClaudeThreadReplay(t, errExecute)
			for _, called := range tc.executor.ExecuteCalls() {
				if called != tc.ownerID {
					t.Fatalf("continuation reached non-owner credential %q", called)
				}
			}
		})
		t.Run(c.name+"/stream-status", func(t *testing.T) {
			tc := newClaudeThreadOwnerTest(t, c.ownerErr, c.disabled)
			direct := &directStreamErrorExecutor{authFallbackExecutor: tc.executor}
			tc.manager.RegisterExecutor(direct)
			req, opts := tc.continuation(t)
			_, errStream := tc.manager.ExecuteStream(context.Background(), []string{"claude"}, req, opts)
			assertClaudeThreadReplay(t, errStream)
		})
		t.Run(c.name+"/stream-bootstrap", func(t *testing.T) {
			tc := newClaudeThreadOwnerTest(t, c.ownerErr, c.disabled)
			req, opts := tc.continuation(t)
			result, errStream := tc.manager.ExecuteStream(context.Background(), []string{"claude"}, req, opts)
			if errStream == nil && result != nil {
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						errStream = chunk.Err
					}
				}
			}
			assertClaudeThreadReplay(t, errStream)
			for _, called := range tc.executor.StreamCalls() {
				if called != tc.ownerID {
					t.Fatalf("continuation reached non-owner credential %q", called)
				}
			}
		})
	}
}

func TestClaudeThreadContinuationKeepsOwnerTransientAndRequestErrors(t *testing.T) {
	cases := []struct {
		name     string
		ownerErr error
		status   int
	}{
		{name: "overloaded", ownerErr: &Error{HTTPStatus: 529, Message: "overloaded_error: Overloaded"}, status: 529},
		{name: "server error", ownerErr: &Error{HTTPStatus: http.StatusInternalServerError, Message: "api_error: boom"}, status: http.StatusInternalServerError},
		{name: "request fault", ownerErr: &requestScopedStatusError{status: http.StatusBadRequest, message: "invalid_request_error: bad block"}, status: http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tc := newClaudeThreadOwnerTest(t, c.ownerErr, false)
			req, opts := tc.continuation(t)
			_, errExecute := tc.manager.Execute(context.Background(), []string{"claude"}, req, opts)
			if errExecute == nil {
				t.Fatal("expected owner error, got success")
			}
			if status := statusCodeFromError(errExecute); status != c.status {
				t.Fatalf("status = %d, want %d; err=%v", status, c.status, errExecute)
			}
		})
	}
}

func TestClaudeThreadOwnerUnavailableDoesNotAffectUnpinnedRequests(t *testing.T) {
	tc := newClaudeThreadOwnerTest(t, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate_limit_error"}, false)
	req := cliproxyexecutor.Request{Model: tc.model, Payload: []byte(`{"model":"` + tc.model + `","thread":{"type":"create"},"messages":[]}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
	for range 2 {
		resp, errExecute := tc.manager.Execute(context.Background(), []string{"claude"}, req, opts)
		if errExecute != nil {
			t.Fatalf("thread create error = %v, want rotation to the other credential", errExecute)
		}
		if got := string(resp.Payload); got != tc.ownerID && got != tc.otherID {
			t.Fatalf("payload = %q", got)
		}
	}
}

func TestIsClaudeThreadOwnerUnavailable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "model cooldown", err: newModelCooldownError("claude-opus-5-5", "claude", time.Hour), want: true},
		{name: "auth unavailable", err: &Error{Code: "auth_unavailable", Message: "no auth available"}, want: true},
		{name: "auth not found", err: &Error{Code: "auth_not_found", Message: "no auth available"}, want: true},
		{name: "rate limited", err: &Error{HTTPStatus: http.StatusTooManyRequests}, want: true},
		{name: "payment required", err: &Error{HTTPStatus: http.StatusPaymentRequired}, want: true},
		{name: "canceled", err: context.Canceled, want: false},
		{name: "server error", err: &Error{HTTPStatus: http.StatusInternalServerError}, want: false},
		{name: "request scoped 429", err: &requestScopedStatusError{status: http.StatusTooManyRequests}, want: false},
		{name: "home concurrency busy", err: NewHomeConcurrencyBusyError("owner at concurrency limit", 2*time.Second), want: false},
		{name: "transient rate limit", err: &Error{Code: ErrorCodeTransientRateLimit, HTTPStatus: http.StatusTooManyRequests}, want: false},
		{name: "plain", err: errors.New("boom"), want: false},
	}
	for _, c := range cases {
		if got := isClaudeThreadOwnerUnavailable(c.err); got != c.want {
			t.Errorf("%s: isClaudeThreadOwnerUnavailable = %v, want %v", c.name, got, c.want)
		}
	}
}

// A configured request-scoped stop rule marks the owner's error as a request
// fault; the replay rewrite must not override it after the marker is unwrapped.
func TestClaudeThreadContinuationHonorsRequestScopedStopRule(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	newManager := func(t *testing.T) (*Manager, string, string) {
		m := NewManager(nil, nil, nil)
		ownerID, otherID, model := t.Name()+"-owner", t.Name()+"-other", "claude-thread-stop-model"
		reg := registry.GetGlobalRegistry()
		for _, auth := range []*Auth{
			{ID: ownerID, Provider: "claude", Status: StatusActive, Metadata: map[string]any{
				"request_scoped_errors": []internalconfig.RequestScopedErrorRule{
					{Status: http.StatusForbidden, Match: []string{"blocked by policy"}, Action: "stop"},
				},
			}},
			{ID: otherID, Provider: "claude", Status: StatusActive},
		} {
			reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
			authID := auth.ID
			t.Cleanup(func() { reg.UnregisterClient(authID) })
			if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register %s: %v", auth.ID, errRegister)
			}
		}
		return m, ownerID, model
	}
	continuation := func(t *testing.T, ownerID, model string) (cliproxyexecutor.Request, cliproxyexecutor.Options) {
		messageID := "msg_" + t.Name()
		opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
		req := cliproxyexecutor.Request{
			Model:   model,
			Payload: []byte(`{"model":"` + model + `","thread":{"type":"continue","previous_message_id":"` + messageID + `"},"messages":[]}`),
		}
		cliproxysession.RememberClaudeThreadAuth(cliproxysession.ClaudeThreadCallerScope("", opts.Metadata, req.Metadata), messageID, ownerID)
		return req, opts
	}
	policyErr := customStatusError{code: http.StatusForbidden, msg: "permission_error: blocked by policy"}

	t.Run("execute", func(t *testing.T) {
		m, ownerID, model := newManager(t)
		m.RegisterExecutor(&mockCustomErrorExecutor{
			identifier: "claude",
			executeFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				return cliproxyexecutor.Response{}, policyErr
			},
		})
		req, opts := continuation(t, ownerID, model)
		_, errExecute := m.Execute(context.Background(), []string{"claude"}, req, opts)
		if _, ok := errExecute.(customStatusError); !ok {
			t.Fatalf("Execute() error = %T/%v, want the original request-stop error", errExecute, errExecute)
		}
	})
	t.Run("stream", func(t *testing.T) {
		m, ownerID, model := newManager(t)
		m.RegisterExecutor(&customStreamMockExecutor{
			identifier: "claude",
			streamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				return nil, policyErr
			},
		})
		req, opts := continuation(t, ownerID, model)
		_, errStream := m.ExecuteStream(context.Background(), []string{"claude"}, req, opts)
		if _, ok := errStream.(customStatusError); !ok {
			t.Fatalf("ExecuteStream() error = %T/%v, want the original request-stop error", errStream, errStream)
		}
	})
}
