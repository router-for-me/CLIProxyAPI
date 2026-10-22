package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func invokeSameUpstreamRetry(ctx context.Context, m *Manager, kind, model string, opts cliproxyexecutor.Options) error {
	req := cliproxyexecutor.Request{Model: model}
	provider := "same-upstream"
	if m.HomeEnabled() {
		provider = "home-retry-contract"
	}
	switch kind {
	case "execute":
		_, errExecute := m.Execute(ctx, []string{provider}, req, opts)
		return errExecute
	case "count":
		_, errCount := m.ExecuteCount(ctx, []string{provider}, req, opts)
		return errCount
	default:
		opts.Stream = true
		stream, errStream := m.ExecuteStream(ctx, []string{provider}, req, opts)
		if errStream != nil {
			return errStream
		}
		var streamErr error
		for chunk := range stream.Chunks {
			if chunk.Err != nil {
				streamErr = chunk.Err
			}
		}
		return streamErr
	}
}

func TestManagerSameUpstreamRetry(t *testing.T) {
	withQuotaCooldownEnabled(t)
	previous := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(60)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(previous) })
	for _, kind := range []string{"execute", "count", "stream", "bootstrap"} {
		for _, tc := range []struct {
			name       string
			retries    int
			failures   int
			err        error
			wantCalls  []string
			wantBound  string
			wantFailed bool
		}{
			{"default", 0, 1, &Error{HTTPStatus: 503}, []string{"a", "b"}, "b", true},
			{"recovers", 2, 2, &Error{HTTPStatus: 503}, []string{"a", "a", "a"}, "a", false},
			{"exhausted", 2, 3, &Error{HTTPStatus: 503}, []string{"a", "a", "a", "b"}, "b", true},
			{"transport", 2, 1, io.ErrUnexpectedEOF, []string{"a", "a"}, "a", false},
			{"configured-handshake-timeout", 1, 1, cliproxyexecutor.NewConfiguredTLSHandshakeTimeoutError(context.DeadlineExceeded), []string{"a", "a"}, "a", false},
			{"wrapped-configured-handshake-timeout", 1, 1, fmt.Errorf("handshake: %w", cliproxyexecutor.NewConfiguredTLSHandshakeTimeoutError(context.DeadlineExceeded)), []string{"a", "a"}, "a", false},
			{"forbidden", 2, 1, &Error{HTTPStatus: 403}, []string{"a", "b"}, "b", true},
			{"unauthorized", 2, 1, &Error{HTTPStatus: 401}, []string{"a", "b"}, "b", true},
			{"quota", 2, 1, &Error{HTTPStatus: 429}, []string{"a", "b"}, "b", true},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				selector := NewSessionAffinitySelector(&FillFirstSelector{})
				t.Cleanup(selector.Stop)
				manager := NewManager(nil, selector, nil)
				manager.SetConfig(&internalconfig.Config{SameUpstreamRetry: tc.retries})
				manager.SetRetryConfig(0, 0, 0)
				model := "same-upstream-" + t.Name()
				for _, id := range []string{"a", "b"} {
					registry.GetGlobalRegistry().RegisterClient(id, "same-upstream", []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
					if _, errRegister := manager.Register(context.Background(), &Auth{ID: id, Provider: "same-upstream"}); errRegister != nil {
						t.Fatal(errRegister)
					}
				}
				var calls []string
				execute := func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					calls = append(calls, auth.ID)
					if req.Model != model {
						t.Fatalf("upstream model = %q, want %q", req.Model, model)
					}
					if auth.ID == "a" && len(calls) > 1 && len(calls) <= tc.retries+1 {
						assertNoCooldown(t, manager, "a", model)
					}
					if auth.ID == "a" && len(calls) <= tc.failures {
						return cliproxyexecutor.Response{}, tc.err
					}
					return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
				}
				manager.RegisterExecutor(&customStreamMockExecutor{
					identifier:              "same-upstream",
					mockCustomErrorExecutor: mockCustomErrorExecutor{executeFn: execute, countFn: execute},
					streamFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
						resp, errExecute := execute(ctx, auth, req, opts)
						if errExecute != nil && kind != "bootstrap" {
							return nil, errExecute
						}
						chunks := make(chan cliproxyexecutor.StreamChunk, 1)
						chunks <- cliproxyexecutor.StreamChunk{Payload: resp.Payload, Err: errExecute}
						close(chunks)
						return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
					},
				})
				opts := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": []string{"stable"}}}
				if errExecute := invokeSameUpstreamRetry(context.Background(), manager, kind, model, opts); errExecute != nil {
					t.Fatalf("execution failed: %v", errExecute)
				}
				if !reflect.DeepEqual(calls, tc.wantCalls) {
					t.Fatalf("calls = %v, want %v", calls, tc.wantCalls)
				}
				if !tc.wantFailed {
					assertNoCooldown(t, manager, "a", model)
				}
				if errExecute := invokeSameUpstreamRetry(context.Background(), manager, kind, model, opts); errExecute != nil {
					t.Fatal(errExecute)
				}
				if got := calls[len(calls)-1]; got != tc.wantBound {
					t.Fatalf("next session auth = %q, want %q", got, tc.wantBound)
				}
			})
		}
	}
}

func TestSameUpstreamRetrySuccessOrdersConcurrentCooldown(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, kind := range []string{"execute", "count", "stream", "bootstrap", "credits"} {
		for _, cooldownAfterRetryDispatch := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cooldown-after-retry-dispatch=%t", kind, cooldownAfterRetryDispatch), func(t *testing.T) {
				manager := NewManager(nil, &FillFirstSelector{}, nil)
				manager.SetConfig(&internalconfig.Config{SameUpstreamRetry: 1, QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true}})
				manager.SetRetryConfig(0, 0, 0)
				authID, model := t.Name(), "claude-retry-cooldown-order"
				provider := "same-upstream"
				if kind == "credits" {
					provider = "antigravity"
				}
				if _, errRegister := manager.Register(context.Background(), &Auth{ID: authID, Provider: provider, Status: StatusActive}); errRegister != nil {
					t.Fatal(errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

				calls := 0
				execute := func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					calls++
					if (calls == 1 && !cooldownAfterRetryDispatch) || (calls == 2 && cooldownAfterRetryDispatch) {
						// Model another dispatch recording a quota error while this attempt is in flight.
						manager.MarkResult(ctx, Result{
							AuthID: authID, Provider: provider, Model: model,
							ExecutionSeq: manager.nextExecutionSeq(),
							Error:        &Error{HTTPStatus: http.StatusTooManyRequests, Retryable: true},
						})
					}
					if calls == 1 {
						return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusServiceUnavailable}
					}
					return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
				}
				manager.RegisterExecutor(&customStreamMockExecutor{
					identifier:              provider,
					mockCustomErrorExecutor: mockCustomErrorExecutor{executeFn: execute, countFn: execute},
					streamFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
						resp, errExecute := execute(ctx, auth, req, opts)
						if errExecute != nil && kind != "bootstrap" {
							return nil, errExecute
						}
						chunks := make(chan cliproxyexecutor.StreamChunk, 1)
						chunks <- cliproxyexecutor.StreamChunk{Payload: resp.Payload, Err: errExecute}
						close(chunks)
						return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
					},
				})
				if kind == "credits" {
					if _, handled, errExecute := manager.tryAntigravityCreditsExecute(context.Background(), cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errExecute != nil || !handled {
						t.Fatalf("credits fallback handled=%t, error=%v", handled, errExecute)
					}
				} else if errExecute := invokeSameUpstreamRetry(context.Background(), manager, kind, model, cliproxyexecutor.Options{}); errExecute != nil {
					t.Fatal(errExecute)
				}
				if calls != 2 {
					t.Fatalf("calls = %d, want 2", calls)
				}
				updated, ok := manager.GetByID(authID)
				if !ok || updated == nil || updated.ModelStates[model] == nil {
					t.Fatal("expected model state after execution")
				}
				state := updated.ModelStates[model]
				if cooldownAfterRetryDispatch {
					if !state.Unavailable || !state.Quota.Exceeded {
						t.Fatal("retry success cleared a cooldown recorded after its dispatch")
					}
				} else if state.Unavailable || state.Quota.Exceeded || !state.NextRetryAfter.IsZero() {
					t.Fatal("retry success did not clear a cooldown recorded before its dispatch")
				}
			})
		}
	}
}

func TestHomeSameUpstreamRetryKeepsSelection(t *testing.T) {
	for _, kind := range []string{"execute", "count", "stream", "bootstrap"} {
		t.Run(kind, func(t *testing.T) {
			dispatcher := &retryContractHomeDispatcher{authIDs: []string{"home-retry-a", "home-retry-b"}}
			executor := &retryContractHomeExecutor{failure: &Error{HTTPStatus: 503}, streamBootstrap: kind == "bootstrap"}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{SameUpstreamRetry: 2, Home: internalconfig.HomeConfig{Enabled: true}})
			manager.SetRetryConfig(0, 0, 0)
			scopes := executionregistry.New()
			manager.PublishHomeDispatch(dispatcher, scopes, 1)
			manager.RegisterExecutor(executor)
			if errExecute := invokeSameUpstreamRetry(context.Background(), manager, kind, "gpt", cliproxyexecutor.Options{}); errExecute != nil {
				t.Fatal(errExecute)
			}
			want := []string{"home-retry-a", "home-retry-a", "home-retry-a", "home-retry-b"}
			if got := executor.Calls(); !reflect.DeepEqual(got, want) {
				t.Fatalf("calls = %v, want %v", got, want)
			}
			if got := dispatcher.Excluded(); len(got) != 2 || len(got[0]) != 0 || !reflect.DeepEqual(got[1], []string{"home-retry-a"}) {
				t.Fatalf("dispatch exclusions = %v, want one selection per credential", got)
			}
			if errDrain := scopes.Drain(context.Background()); errDrain != nil {
				t.Fatal(errDrain)
			}
		})
	}
}

func TestSameUpstreamRetryHonorsRequestTermination(t *testing.T) {
	for _, kind := range []string{"execute", "count", "stream", "bootstrap"} {
		for _, failure := range []string{"canceled", "request-fault", "stop-rule"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				manager := NewManager(nil, &FillFirstSelector{}, nil)
				manager.SetConfig(&internalconfig.Config{SameUpstreamRetry: 3})
				manager.SetRetryConfig(0, 0, 0)
				registerRetryRoundLocalAuths(t, manager, "same-upstream", "termination", map[string]int{"a": 0, "b": 0})
				if failure == "stop-rule" {
					auth, _ := manager.GetByID("a")
					auth.Metadata["request_scoped_errors"] = []internalconfig.RequestScopedErrorRule{{Status: 503, Match: []string{"stop this request"}, Action: "stop"}}
					if _, errUpdate := manager.Update(context.Background(), auth); errUpdate != nil {
						t.Fatal(errUpdate)
					}
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				calls := 0
				execute := func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					calls++
					errFailure := &Error{HTTPStatus: 503, Message: "stop this request"}
					if failure == "canceled" {
						cancel()
					} else if failure == "request-fault" {
						errFailure.HTTPStatus = 400
						errFailure.Message = `{"error":{"type":"invalid_request_error","message":"Invalid request parameter"}}`
					}
					return cliproxyexecutor.Response{}, errFailure
				}
				manager.RegisterExecutor(&customStreamMockExecutor{
					identifier:              "same-upstream",
					mockCustomErrorExecutor: mockCustomErrorExecutor{executeFn: execute, countFn: execute},
					streamFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
						_, errExecute := execute(ctx, auth, req, opts)
						if kind != "bootstrap" {
							return nil, errExecute
						}
						chunks := make(chan cliproxyexecutor.StreamChunk, 1)
						chunks <- cliproxyexecutor.StreamChunk{Err: errExecute}
						close(chunks)
						return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
					},
				})
				if errExecute := invokeSameUpstreamRetry(ctx, manager, kind, "termination", cliproxyexecutor.Options{}); errExecute == nil {
					t.Fatal("request should fail")
				} else if failure == "canceled" && !errors.Is(errExecute, context.Canceled) {
					t.Fatalf("error = %v, want context.Canceled", errExecute)
				}
				if calls != 1 {
					t.Fatalf("calls = %d, want 1", calls)
				}
			})
		}
	}
}

func TestSameUpstreamRetryComposesWithRoundsAndCredentialCap(t *testing.T) {
	for _, kind := range []string{"execute", "count", "stream"} {
		t.Run(kind, func(t *testing.T) {
			manager := NewManager(nil, &FillFirstSelector{}, nil)
			manager.SetConfig(&internalconfig.Config{SameUpstreamRetry: 2})
			manager.SetRetryConfig(1, 0, 1)
			registerRetryRoundLocalAuths(t, manager, "same-upstream", "retry-cap", map[string]int{"a": 1, "b": 1})
			executor := &retryRoundCallExecutor{identifier: "same-upstream"}
			manager.RegisterExecutor(executor)
			if errExecute := invokeSameUpstreamRetry(context.Background(), manager, kind, "retry-cap", cliproxyexecutor.Options{}); errExecute == nil {
				t.Fatal("request should exhaust both rounds")
			}
			if got := executor.ids(kind); !reflect.DeepEqual(got, []string{"a", "a", "a", "a", "a", "a"}) {
				t.Fatalf("calls = %v, want 3 attempts in each of 2 rounds on one credential", got)
			}
			manager.SetConfig(&internalconfig.Config{SameUpstreamRetry: 0})
			before := len(executor.ids(kind))
			_ = invokeSameUpstreamRetry(context.Background(), manager, kind, "retry-cap", cliproxyexecutor.Options{})
			if additional := len(executor.ids(kind)) - before; additional != 2 {
				t.Fatalf("calls after config update = %d, want 2", additional)
			}
		})
	}
}

func TestSameUpstreamRetryRejectsReturnedCancellation(t *testing.T) {
	for _, kind := range []string{"execute", "count", "stream", "bootstrap"} {
		for _, errFailure := range []error{
			context.Canceled,
			context.DeadlineExceeded,
			fmt.Errorf("wrapped: %w", context.Canceled),
			fmt.Errorf("wrapped: %w", context.DeadlineExceeded),
			errors.Join(cliproxyexecutor.NewConfiguredTLSHandshakeTimeoutError(context.DeadlineExceeded), context.Canceled),
			errors.Join(cliproxyexecutor.NewConfiguredTLSHandshakeTimeoutError(context.DeadlineExceeded), context.DeadlineExceeded),
		} {
			t.Run(kind+"/"+errFailure.Error(), func(t *testing.T) {
				manager := NewManager(nil, &FillFirstSelector{}, nil)
				manager.SetConfig(&internalconfig.Config{SameUpstreamRetry: 3})
				manager.SetRetryConfig(0, 0, 1)
				registerRetryRoundLocalAuths(t, manager, "same-upstream", "returned-cancel", map[string]int{"a": 0})
				calls := 0
				execute := func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					calls++
					return cliproxyexecutor.Response{}, errFailure
				}
				manager.RegisterExecutor(&customStreamMockExecutor{
					identifier:              "same-upstream",
					mockCustomErrorExecutor: mockCustomErrorExecutor{executeFn: execute, countFn: execute},
					streamFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
						_, errExecute := execute(ctx, auth, req, opts)
						if kind != "bootstrap" {
							return nil, errExecute
						}
						chunks := make(chan cliproxyexecutor.StreamChunk, 1)
						chunks <- cliproxyexecutor.StreamChunk{Err: errExecute}
						close(chunks)
						return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
					},
				})
				if errExecute := invokeSameUpstreamRetry(context.Background(), manager, kind, "returned-cancel", cliproxyexecutor.Options{}); !errors.Is(errExecute, errFailure) {
					t.Fatalf("error = %v, want %v", errExecute, errFailure)
				}
				if calls != 1 {
					t.Fatalf("calls = %d, want 1 for returned cancellation", calls)
				}
			})
		}
	}
}

func TestHomeSameUpstreamRetrySharesTransportBudget(t *testing.T) {
	for _, retries := range []int{0, 2} {
		t.Run(fmt.Sprint(retries), func(t *testing.T) {
			dispatcher := &retryContractHomeDispatcher{authIDs: []string{"home-retry-a", "home-retry-b"}}
			executor := &retryContractHomeExecutor{failure: io.ErrUnexpectedEOF}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{SameUpstreamRetry: retries, Home: internalconfig.HomeConfig{Enabled: true}})
			manager.SetRetryConfig(0, 0, 0)
			scopes := executionregistry.New()
			manager.PublishHomeDispatch(dispatcher, scopes, 1)
			manager.RegisterExecutor(executor)
			if errExecute := invokeSameUpstreamRetry(context.Background(), manager, "stream", "gpt", cliproxyexecutor.Options{}); errExecute != nil {
				t.Fatal(errExecute)
			}
			want := []string{"home-retry-a", "home-retry-a"}
			for range retries {
				want = append(want, "home-retry-a")
			}
			want = append(want, "home-retry-b")
			if got := executor.Calls(); !reflect.DeepEqual(got, want) {
				t.Fatalf("calls = %v, want %v", got, want)
			}
			if errDrain := scopes.Drain(context.Background()); errDrain != nil {
				t.Fatal(errDrain)
			}
		})
	}
}

func TestAntigravityCreditsSameUpstreamRetry(t *testing.T) {
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.SetConfig(&internalconfig.Config{SameUpstreamRetry: 2, QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true}})
	manager.SetRetryConfig(0, 0, 0)
	const authID, model = "same-upstream-credits", "claude-same-upstream-credits"
	registry.GetGlobalRegistry().RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: authID, Provider: "antigravity"}); errRegister != nil {
		t.Fatal(errRegister)
	}
	creditCalls := 0
	manager.RegisterExecutor(&mockCustomErrorExecutor{
		identifier: "antigravity",
		executeFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			if !AntigravityCreditsRequested(ctx) {
				return cliproxyexecutor.Response{}, &Error{HTTPStatus: 429}
			}
			creditCalls++
			if auth.ID != authID || req.Model != model {
				t.Fatalf("credits attempt changed target: auth=%s model=%s", auth.ID, req.Model)
			}
			current, _ := manager.GetByID(authID)
			if current.Failed != 1 {
				t.Fatalf("failures recorded during credits retry = %d, want only initial quota failure", current.Failed)
			}
			if creditCalls < 3 {
				return cliproxyexecutor.Response{}, &Error{HTTPStatus: 503}
			}
			return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
		},
	})
	if _, errExecute := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errExecute != nil {
		t.Fatal(errExecute)
	}
	if creditCalls != 3 {
		t.Fatalf("credits attempts = %d, want 3", creditCalls)
	}
}

func TestManagerSameUpstreamRetryStopsAfterPayload(t *testing.T) {
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.SetConfig(&internalconfig.Config{SameUpstreamRetry: 3})
	manager.SetRetryConfig(0, 0, 0)
	registerRetryRoundLocalAuths(t, manager, "same-upstream", "partial-stream", map[string]int{"a": 0, "b": 0})
	calls := 0
	errFailure := &Error{HTTPStatus: 503}
	manager.RegisterExecutor(&customStreamMockExecutor{
		identifier: "same-upstream",
		streamFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			calls++
			chunks := make(chan cliproxyexecutor.StreamChunk, 2)
			chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("first")}
			chunks <- cliproxyexecutor.StreamChunk{Err: errFailure}
			close(chunks)
			return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
		},
	})
	if errStream := invokeSameUpstreamRetry(context.Background(), manager, "stream", "partial-stream", cliproxyexecutor.Options{}); !errors.Is(errStream, errFailure) {
		t.Fatalf("stream error = %v, want %v", errStream, errFailure)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 after first payload", calls)
	}
}

func TestSameUpstreamRetrySharesActualModelBudget(t *testing.T) {
	for _, kind := range []string{"execute", "count", "stream"} {
		t.Run(kind, func(t *testing.T) {
			const alias, actual = "same-upstream-alias", "same-upstream-actual"
			models := []internalconfig.OpenAICompatibilityModel{{Name: "pool-a", Alias: alias}, {Name: "pool-b", Alias: alias}}
			exec := &openAICompatPoolExecutor{id: openAICompatPoolProviderKey,
				executeErrors:     map[string]error{actual: &Error{HTTPStatus: 503}},
				countErrors:       map[string]error{actual: &Error{HTTPStatus: 503}},
				streamFirstErrors: map[string]error{actual: &Error{HTTPStatus: 503}}}
			manager := newOpenAICompatPoolTestManager(t, alias, models, exec)
			manager.SetConfig(&internalconfig.Config{SameUpstreamRetry: 2, OpenAICompatibility: []internalconfig.OpenAICompatibility{{Name: "pool", Models: models}}})
			manager.SetRetryConfig(0, 0, 0)
			opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.AuthSelectionModelMetadataKey: alias}}
			req := cliproxyexecutor.Request{Model: actual}
			switch kind {
			case "execute":
				_, _ = manager.Execute(context.Background(), []string{openAICompatPoolProviderKey}, req, opts)
			case "count":
				_, _ = manager.ExecuteCount(context.Background(), []string{openAICompatPoolProviderKey}, req, opts)
			default:
				_, _ = manager.ExecuteStream(context.Background(), []string{openAICompatPoolProviderKey}, req, opts)
			}
			var calls []string
			switch kind {
			case "execute":
				calls = exec.ExecuteModels()
			case "count":
				calls = exec.CountModels()
			default:
				calls = exec.StreamModels()
			}
			t.Logf("same actual target calls: %v", calls)
			if len(calls) != 4 {
				t.Fatalf("same auth/model calls=%d, want 2 existing candidate attempts + 2 shared extra attempts", len(calls))
			}
		})
	}
}
