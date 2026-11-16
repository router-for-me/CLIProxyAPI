package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type issue6865Executor struct {
	id           string
	executeFn    func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	streamFn     func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
	countFn      func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	refreshCalls int
	nextToken    string
}

func (e *issue6865Executor) Identifier() string { return e.id }

func (e *issue6865Executor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.executeFn != nil {
		return e.executeFn(ctx, auth, req, opts)
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *issue6865Executor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.streamFn != nil {
		return e.streamFn(ctx, auth, req, opts)
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"ok":true}`)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *issue6865Executor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.countFn != nil {
		return e.countFn(ctx, auth, req, opts)
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"total_tokens":1}`)}, nil
}

func (e *issue6865Executor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	e.refreshCalls++
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	tok := e.nextToken
	if tok == "" {
		tok = "refreshed-access-token"
	}
	updated.Metadata["access_token"] = tok
	return updated, nil
}

func (e *issue6865Executor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestIssue6865_RequestScoped401_AccessTokenOnly_RemainsSelectable(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	for _, action := range []string{"continue", "stop"} {
		t.Run(action, func(t *testing.T) {
			cfg := &internalconfig.Config{
				OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
					"codex": {
						{Status: http.StatusUnauthorized, Match: []string{"marker"}, Action: action},
					},
				},
			}
			m := NewManager(nil, nil, nil)
			m.SetConfig(cfg)

			authID := "issue6865-access-only-" + action
			auth := &Auth{
				ID:         authID,
				Provider:   "codex",
				Status:     StatusActive,
				Attributes: map[string]string{"auth_kind": "oauth"},
				Metadata:   map[string]any{"access_token": "only-access-token"},
			}
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

			if _, errReg := m.Register(context.Background(), auth); errReg != nil {
				t.Fatalf("Register(%s) error = %v", auth.ID, errReg)
			}

			calls := 0
			exec := &issue6865Executor{
				id: "codex",
				executeFn: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					calls++
					if calls == 1 {
						return cliproxyexecutor.Response{}, &Error{
							HTTPStatus: http.StatusUnauthorized,
							Message:    `{"error":"marker"}`,
						}
					}
					return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
				},
			}
			m.RegisterExecutor(exec)

			req := cliproxyexecutor.Request{Model: "gpt-5.5"}
			opts := cliproxyexecutor.Options{}

			if _, errExec := m.Execute(context.Background(), []string{"codex"}, req, opts); errExec == nil {
				t.Fatal("first Execute expected 401 error, got nil")
			}
			if calls != 1 {
				t.Fatalf("first Execute upstream calls = %d, want 1", calls)
			}

			got, ok := m.GetByID(auth.ID)
			if !ok || got == nil {
				t.Fatalf("GetByID(%s) not found", auth.ID)
			}
			if got.RejectedAccessToken != "" {
				t.Fatalf("RejectedAccessToken = %q, want empty after rule-matched 401", got.RejectedAccessToken)
			}

			resp, errExec2 := m.Execute(context.Background(), []string{"codex"}, req, opts)
			if errExec2 != nil {
				t.Fatalf("second Execute error = %v, want nil (credential should stay selectable)", errExec2)
			}
			if string(resp.Payload) != `{"ok":true}` {
				t.Fatalf("second Execute payload = %q, want %q", string(resp.Payload), `{"ok":true}`)
			}
		})
	}
}

func TestIssue6865_RequestScoped401_Refreshable_NoRefreshOrSameCredentialRetry(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	t.Run("execute_stop", func(t *testing.T) {
		cfg := &internalconfig.Config{
			OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
				"codex": {
					{Status: http.StatusUnauthorized, Match: []string{"marker"}, Action: "stop"},
				},
			},
		}
		m := NewManager(nil, nil, nil)
		m.SetConfig(cfg)

		auth := &Auth{
			ID:         "issue6865-refreshable-stop",
			Provider:   "codex",
			Status:     StatusActive,
			Attributes: map[string]string{"auth_kind": "oauth"},
			Metadata: map[string]any{
				"access_token":  "initial-access-token",
				"refresh_token": "valid-refresh-token",
			},
		}
		reg := registry.GetGlobalRegistry()
		reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
		t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

		if _, errReg := m.Register(context.Background(), auth); errReg != nil {
			t.Fatalf("Register error = %v", errReg)
		}

		calls := 0
		exec := &issue6865Executor{
			id: "codex",
			executeFn: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				calls++
				if calls == 1 {
					return cliproxyexecutor.Response{}, &Error{
						HTTPStatus: http.StatusUnauthorized,
						Message:    `{"error":"marker"}`,
					}
				}
				return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
			},
		}
		m.RegisterExecutor(exec)

		_, errExec := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
		if errExec == nil {
			t.Fatal("Execute expected 401 error from stop rule, got nil")
		}
		if exec.refreshCalls != 0 {
			t.Fatalf("refreshCalls = %d, want 0 for rule-matched 401", exec.refreshCalls)
		}
		if calls != 1 {
			t.Fatalf("execute calls = %d, want 1 (no same-credential retry)", calls)
		}
		got, _ := m.GetByID(auth.ID)
		if got.RejectedAccessToken != "" {
			t.Fatalf("RejectedAccessToken = %q, want empty", got.RejectedAccessToken)
		}
	})

	t.Run("count_tokens_and_stream_paths", func(t *testing.T) {
		cfg := &internalconfig.Config{
			OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
				"codex": {
					{Status: http.StatusUnauthorized, Match: []string{"marker"}, Action: "stop"},
				},
			},
		}
		m := NewManager(nil, nil, nil)
		m.SetConfig(cfg)

		auth := &Auth{
			ID:         "issue6865-refreshable-paths",
			Provider:   "codex",
			Status:     StatusActive,
			Attributes: map[string]string{"auth_kind": "oauth"},
			Metadata: map[string]any{
				"access_token":  "initial-access-token",
				"refresh_token": "valid-refresh-token",
			},
		}
		reg := registry.GetGlobalRegistry()
		reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
		t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

		if _, errReg := m.Register(context.Background(), auth); errReg != nil {
			t.Fatalf("Register error = %v", errReg)
		}

		countCalls := 0
		streamCalls := 0
		bootstrapMode := false
		exec := &issue6865Executor{
			id: "codex",
			countFn: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				countCalls++
				return cliproxyexecutor.Response{}, &Error{
					HTTPStatus: http.StatusUnauthorized,
					Message:    `{"error":"marker"}`,
				}
			},
			streamFn: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				streamCalls++
				if !bootstrapMode {
					return nil, &Error{
						HTTPStatus: http.StatusUnauthorized,
						Message:    `{"error":"marker"}`,
					}
				}
				ch := make(chan cliproxyexecutor.StreamChunk, 1)
				ch <- cliproxyexecutor.StreamChunk{
					Err: &Error{
						HTTPStatus: http.StatusUnauthorized,
						Message:    `{"error":"marker"}`,
					},
				}
				close(ch)
				return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
			},
		}
		m.RegisterExecutor(exec)

		req := cliproxyexecutor.Request{Model: "gpt-5.5"}
		opts := cliproxyexecutor.Options{}

		if _, errCount := m.ExecuteCount(context.Background(), []string{"codex"}, req, opts); errCount == nil {
			t.Fatal("ExecuteCount expected 401 error, got nil")
		}
		if countCalls != 1 || exec.refreshCalls != 0 {
			t.Fatalf("after CountTokens: countCalls=%d refreshCalls=%d, want 1 and 0", countCalls, exec.refreshCalls)
		}

		if _, errStream := m.ExecuteStream(context.Background(), []string{"codex"}, req, opts); errStream == nil {
			t.Fatal("ExecuteStream (initial) expected 401 error, got nil")
		}
		if streamCalls != 1 || exec.refreshCalls != 0 {
			t.Fatalf("after ExecuteStream (initial): streamCalls=%d refreshCalls=%d, want 1 and 0", streamCalls, exec.refreshCalls)
		}

		bootstrapMode = true
		if _, errStream := m.ExecuteStream(context.Background(), []string{"codex"}, req, opts); errStream == nil {
			t.Fatal("ExecuteStream (bootstrap) expected 401 error, got nil")
		}
		if streamCalls != 2 || exec.refreshCalls != 0 {
			t.Fatalf("after ExecuteStream (bootstrap): streamCalls=%d refreshCalls=%d, want 2 and 0", streamCalls, exec.refreshCalls)
		}

		got, _ := m.GetByID(auth.ID)
		if got.RejectedAccessToken != "" {
			t.Fatalf("RejectedAccessToken = %q, want empty", got.RejectedAccessToken)
		}
	})
}

func TestIssue6865_RequestScoped401_ForceCooldown_CoolsWithoutRejectingOrRefreshing(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	for _, action := range []string{"stop-and-cooldown", "continue-and-cooldown"} {
		t.Run("access_token_only_"+action, func(t *testing.T) {
			cfg := &internalconfig.Config{
				OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
					"codex": {
						{Status: http.StatusUnauthorized, Match: []string{"marker"}, Action: action},
					},
				},
			}
			m := NewManager(nil, nil, nil)
			m.SetConfig(cfg)

			authID := "issue6865-force-cooldown-only-" + action
			auth := &Auth{
				ID:         authID,
				Provider:   "codex",
				Status:     StatusActive,
				Attributes: map[string]string{"auth_kind": "oauth"},
				Metadata:   map[string]any{"access_token": "only-access-token"},
			}
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

			if _, errReg := m.Register(context.Background(), auth); errReg != nil {
				t.Fatalf("Register error = %v", errReg)
			}

			exec := &issue6865Executor{
				id: "codex",
				executeFn: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					return cliproxyexecutor.Response{}, &Error{
						HTTPStatus: http.StatusUnauthorized,
						Message:    `{"error":"marker"}`,
					}
				},
			}
			m.RegisterExecutor(exec)

			_, errExec := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
			if errExec == nil {
				t.Fatal("Execute expected 401 error, got nil")
			}

			got, _ := m.GetByID(auth.ID)
			if got.RejectedAccessToken != "" {
				t.Fatalf("RejectedAccessToken = %q, want empty for %s", got.RejectedAccessToken, action)
			}
			st := got.ModelStates["gpt-5.5"]
			if st == nil || st.NextRetryAfter.IsZero() {
				t.Fatalf("ModelStates[gpt-5.5] NextRetryAfter not set: %+v", st)
			}
			if blocked, _, _ := isAuthBlockedForModel(got, "gpt-5.5", st.NextRetryAfter.Add(-time.Second)); !blocked {
				t.Fatal("expected credential to be blocked during cooldown")
			}
			if blocked, reason, _ := isAuthBlockedForModel(got, "gpt-5.5", st.NextRetryAfter.Add(time.Second)); blocked {
				t.Fatalf("expected credential to be selectable after cooldown ends, got blocked=true reason=%v", reason)
			}
		})

		t.Run("refreshable_"+action, func(t *testing.T) {
			cfg := &internalconfig.Config{
				OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
					"codex": {
						{Status: http.StatusUnauthorized, Match: []string{"marker"}, Action: action},
					},
				},
			}
			m := NewManager(nil, nil, nil)
			m.SetConfig(cfg)

			authID := "issue6865-force-cooldown-refreshable-" + action
			auth := &Auth{
				ID:         authID,
				Provider:   "codex",
				Status:     StatusActive,
				Attributes: map[string]string{"auth_kind": "oauth"},
				Metadata: map[string]any{
					"access_token":  "valid-access-token",
					"refresh_token": "valid-refresh-token",
					"expired":       time.Now().Add(8 * 24 * time.Hour).UTC().Format(time.RFC3339),
				},
			}
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

			if _, errReg := m.Register(context.Background(), auth); errReg != nil {
				t.Fatalf("Register error = %v", errReg)
			}

			exec := &issue6865Executor{
				id: "codex",
				executeFn: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					return cliproxyexecutor.Response{}, &Error{
						HTTPStatus: http.StatusUnauthorized,
						Message:    `{"error":"marker"}`,
					}
				},
			}
			m.RegisterExecutor(exec)

			_, errExec := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
			if errExec == nil {
				t.Fatal("Execute expected 401 error, got nil")
			}
			if exec.refreshCalls != 0 {
				t.Fatalf("synchronous refreshCalls = %d, want 0", exec.refreshCalls)
			}

			got, _ := m.GetByID(auth.ID)
			if got.RejectedAccessToken != "" {
				t.Fatalf("RejectedAccessToken = %q, want empty", got.RejectedAccessToken)
			}
			if !got.NextRefreshAfter.IsZero() {
				t.Fatalf("NextRefreshAfter = %v, want zero (no unauthorized refresh scheduled)", got.NextRefreshAfter)
			}

			loop := newAuthAutoRefreshLoop(m, 5*time.Second, 1)
			loop.handleDueAuth(context.Background(), time.Now(), auth.ID)
			select {
			case job := <-loop.jobs:
				t.Fatalf("expected no refresh job enqueued for %s 401 cooldown, got job %+v", action, job)
			default:
			}
		})
	}

	t.Run("direct_and_stale_mark_result", func(t *testing.T) {
		m := NewManager(nil, nil, nil)
		auth := &Auth{
			ID:                "issue6865-direct-mark-result",
			Provider:          "codex",
			Status:            StatusActive,
			CredentialVersion: 2,
			Attributes:        map[string]string{"auth_kind": "oauth"},
			Metadata:          map[string]any{"access_token": "only-access-token"},
		}
		reg := registry.GetGlobalRegistry()
		reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
		t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

		if _, errReg := m.Register(context.Background(), auth); errReg != nil {
			t.Fatalf("Register error = %v", errReg)
		}

		// Stale CredentialVersion result with ErrorCodeForceCooldown must not reject token.
		m.MarkResult(context.Background(), Result{
			AuthID:            auth.ID,
			Provider:          "codex",
			Model:             "gpt-5.5",
			AccessToken:       "only-access-token",
			CredentialVersion: 1,
			Error:             &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusUnauthorized, Message: "marker"},
		})
		got, _ := m.GetByID(auth.ID)
		if got.RejectedAccessToken != "" {
			t.Fatalf("RejectedAccessToken after stale MarkResult = %q, want empty", got.RejectedAccessToken)
		}

		// Current result with ErrorCodeForceCooldown cools model for 30m without rejecting token.
		m.MarkResult(context.Background(), Result{
			AuthID:            auth.ID,
			Provider:          "codex",
			Model:             "gpt-5.5",
			AccessToken:       "only-access-token",
			CredentialVersion: 2,
			Error:             &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusUnauthorized, Message: "marker"},
		})
		got, _ = m.GetByID(auth.ID)
		if got.RejectedAccessToken != "" {
			t.Fatalf("RejectedAccessToken after current MarkResult = %q, want empty", got.RejectedAccessToken)
		}
		st := got.ModelStates["gpt-5.5"]
		if st == nil || st.NextRetryAfter.IsZero() {
			t.Fatalf("ModelStates[gpt-5.5] NextRetryAfter not set: %+v", st)
		}
		if blocked, reason, _ := isAuthBlockedForModel(got, "gpt-5.5", st.NextRetryAfter.Add(time.Second)); blocked {
			t.Fatalf("expected unblocked after cooldown, got blocked=true reason=%v", reason)
		}
	})
}

func TestIssue6865_Unmatched401_StillRejectsAccessTokenOnlyCredential(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	cfg := &internalconfig.Config{
		OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
			"codex": {
				{Status: http.StatusUnauthorized, Match: []string{"marker"}, Action: "continue"},
			},
		},
	}
	m := NewManager(nil, nil, nil)
	m.SetConfig(cfg)

	auth := &Auth{
		ID:         "issue6865-unmatched-401",
		Provider:   "codex",
		Status:     StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth"},
		Metadata:   map[string]any{"access_token": "only-access-token"},
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	if _, errReg := m.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("Register error = %v", errReg)
	}

	exec := &issue6865Executor{
		id: "codex",
		executeFn: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{}, &Error{
				HTTPStatus: http.StatusUnauthorized,
				Message:    "{\"error\":\"different_reason\"}",
			}
		},
	}
	m.RegisterExecutor(exec)

	_, errExec := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.5"}, cliproxyexecutor.Options{})
	if errExec == nil {
		t.Fatal("Execute expected 401 error, got nil")
	}

	got, _ := m.GetByID(auth.ID)
	if got.RejectedAccessToken != "only-access-token" {
		t.Fatalf("RejectedAccessToken = %q, want %q for unmatched 401", got.RejectedAccessToken, "only-access-token")
	}
}
