package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type credentialTerminalResponseError struct{ error }

func (e credentialTerminalResponseError) Unwrap() error          { return e.error }
func (credentialTerminalResponseError) IsCredentialScoped() bool { return true }

func TestManagerTerminalResponsePolicy(t *testing.T) {
	for _, homeMode := range []bool{false, true} {
		for _, mode := range []string{"execute", "count", "stream", "bootstrap"} {
			for _, policy := range []string{"default", "wrapped", "stop", "stop-and-cooldown", "continue", "continue-and-cooldown", "credential", "ordinary"} {
				t.Run(fmt.Sprintf("home=%t/%s/%s", homeMode, mode, policy), func(t *testing.T) {
					terminal := &cliproxyexecutor.RequestTerminatedError{
						HTTPStatus: http.StatusBadGateway,
						Header:     http.Header{"X-Terminal-Response": []string{"preserved"}},
						Body:       []byte(`{"detail":"request outcome is unknown"}`),
					}
					var failure error = terminal
					var rules []internalconfig.RequestScopedErrorRule
					switch policy {
					case "wrapped":
						failure = fmt.Errorf("executor response: %w", terminal)
					case "credential":
						terminal.HTTPStatus = http.StatusTooManyRequests
						failure = credentialTerminalResponseError{error: terminal}
					case "ordinary":
						failure = &Error{HTTPStatus: http.StatusBadGateway, Message: "temporary upstream failure"}
					case "default":
					default:
						rules = []internalconfig.RequestScopedErrorRule{{Status: terminal.HTTPStatus, Match: []string{"outcome is unknown"}, Action: policy}}
					}
					metadata := map[string]any{"request_scoped_errors": rules}
					executor := &retryContractHomeExecutor{failure: failure, streamBootstrap: mode == "bootstrap"}
					manager := NewManager(nil, &FillFirstSelector{}, nil)
					manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: homeMode}})
					manager.SetRetryConfig(1, 0, 2)
					manager.RegisterExecutor(executor)
					executions := executionregistry.New()
					const model = "terminal-response-model"
					if homeMode {
						manager.PublishHomeDispatch(&retryContractHomeDispatcher{authIDs: []string{"home-retry-a", "home-retry-b"}, metadata: metadata}, executions, 1)
					} else {
						for _, id := range []string{"home-retry-a", "home-retry-b"} {
							registry.GetGlobalRegistry().RegisterClient(id, executor.Identifier(), []*registry.ModelInfo{{ID: model}})
							t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
							if _, errRegister := manager.Register(context.Background(), &Auth{ID: id, Provider: executor.Identifier(), Status: StatusActive, Metadata: metadata}); errRegister != nil {
								t.Fatal(errRegister)
							}
						}
					}
					errExecute := invokeTerminalResponse(manager, executor.Identifier(), model, mode)
					stop := policy == "default" || policy == "wrapped" || policy == "stop" || policy == "stop-and-cooldown"
					wantCalls := []string{"home-retry-a"}
					if stop {
						requireTerminalResponse(t, errExecute, terminal)
					} else {
						wantCalls = append(wantCalls, "home-retry-b")
						if errExecute != nil {
							t.Fatalf("safe credential failover failed: %v", errExecute)
						}
					}
					if calls := executor.Calls(); !reflect.DeepEqual(calls, wantCalls) {
						t.Fatalf("credential executions = %v, want %v", calls, wantCalls)
					}
					if active := executions.FreezeInFlight(time.Time{}).Executions; len(active) != 0 {
						t.Fatalf("execution scopes leaked: %v", active)
					}
					if !homeMode && (policy == "default" || policy == "wrapped" || policy == "stop") {
						auth, _ := manager.GetByID("home-retry-a")
						if auth.Unavailable || !auth.NextRetryAfter.IsZero() {
							t.Fatalf("terminal response cooled credential: %+v", auth)
						}
					}
				})
			}
		}
	}
}

func TestHomeTerminalResponseReleasesWithoutAcknowledgement(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream", "bootstrap"} {
		for _, action := range []string{"", "stop", "stop-and-cooldown"} {
			t.Run(mode+"/"+action, func(t *testing.T) {
				terminal := &cliproxyexecutor.RequestTerminatedError{HTTPStatus: http.StatusBadGateway, Body: []byte(`{"detail":"request outcome is unknown"}`)}
				var rules []internalconfig.RequestScopedErrorRule
				if action != "" {
					rules = []internalconfig.RequestScopedErrorRule{{Status: terminal.HTTPStatus, Match: []string{"outcome is unknown"}, Action: action}}
				}
				executor := &retryContractHomeExecutor{failure: terminal, streamBootstrap: mode == "bootstrap"}
				manager := NewManager(nil, nil, nil)
				manager.SetConfig(&internalconfig.Config{
					Home:                  internalconfig.HomeConfig{Enabled: true},
					CredentialConcurrency: internalconfig.CredentialConcurrencyConfig{CPACancelBound: time.Nanosecond},
				})
				manager.SetRetryConfig(1, 0, 2)
				manager.RegisterExecutor(executor)
				executions := executionregistry.New()
				releases := make(chan executionregistry.ReleaseGroup, 2)
				executions.SetReleaseSink(func(group executionregistry.ReleaseGroup, sequence int64) *executionregistry.ReleaseTicket {
					releases <- group
					return executionregistry.NewReleaseTicket(group, sequence, make(chan struct{}))
				})
				dispatcher := &accountedHomeExecutionDispatcher{auths: []Auth{{ID: "home-retry-a", Provider: executor.Identifier(), Status: StatusActive, Metadata: map[string]any{"request_scoped_errors": rules}}}}
				manager.PublishHomeDispatch(dispatcher, executions, 1)
				requireTerminalResponse(t, invokeTerminalResponse(manager, executor.Identifier(), "terminal-response-model", mode), terminal)
				if calls := executor.Calls(); !reflect.DeepEqual(calls, []string{"home-retry-a"}) {
					t.Fatalf("terminal response was replayed: %v", calls)
				}
				if dispatcher.calls.Load() != 1 || len(executions.FreezeInFlight(time.Time{}).Executions) != 0 {
					t.Fatal("terminal execution redispatched or leaked its scope")
				}
				select {
				case group := <-releases:
					if group != (executionregistry.ReleaseGroup{CredentialID: "home-retry-a", Model: "terminal-response-model"}) {
						t.Fatalf("wrong concurrency release: %+v", group)
					}
				default:
					t.Fatal("concurrency admission was not released")
				}
				select {
				case <-releases:
					t.Fatal("concurrency admission released twice")
				default:
				}
			})
		}
	}
}

func TestManagerTerminalUnauthorizedDoesNotRefresh(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream", "bootstrap"} {
		t.Run(mode, func(t *testing.T) {
			terminal := &cliproxyexecutor.RequestTerminatedError{HTTPStatus: http.StatusUnauthorized, Body: []byte(`{"detail":"terminal rejection"}`)}
			executor := &claudeCancellationTestExecutor{
				executeFn: func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
					return cliproxyexecutor.Response{}, terminal
				},
				countFn: func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
					return cliproxyexecutor.Response{}, terminal
				},
				streamFn: func(context.Context, *Auth) (*cliproxyexecutor.StreamResult, error) {
					if mode == "bootstrap" {
						chunks := make(chan cliproxyexecutor.StreamChunk, 1)
						chunks <- cliproxyexecutor.StreamChunk{Err: terminal}
						close(chunks)
						return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
					}
					return nil, terminal
				},
			}
			manager, auth, model := newClaudeCancellationTestManager(t, executor, nil)
			requireTerminalResponse(t, invokeTerminalResponse(manager, executor.Identifier(), model, mode), terminal)
			if calls := executor.executeCalls.Load() + executor.countCalls.Load() + executor.streamCalls.Load(); calls != 1 || executor.refreshCalls.Load() != 0 {
				t.Fatalf("terminal 401 was retried or refreshed: executions=%d refreshes=%d", calls, executor.refreshCalls.Load())
			}
			requireClaudeCancellationNeutral(t, manager, auth.ID, model)
		})
	}
}

func invokeTerminalResponse(manager *Manager, provider, model, mode string) error {
	req := cliproxyexecutor.Request{Model: model}
	switch mode {
	case "execute":
		_, errExecute := manager.Execute(context.Background(), []string{provider}, req, cliproxyexecutor.Options{})
		return errExecute
	case "count":
		_, errCount := manager.ExecuteCount(context.Background(), []string{provider}, req, cliproxyexecutor.Options{})
		return errCount
	default:
		stream, errStream := manager.ExecuteStream(context.Background(), []string{provider}, req, cliproxyexecutor.Options{Stream: true})
		if errStream != nil {
			return errStream
		}
		for chunk := range stream.Chunks {
			if chunk.Err != nil {
				return chunk.Err
			}
		}
		return nil
	}
}

func requireTerminalResponse(t *testing.T, err error, terminal *cliproxyexecutor.RequestTerminatedError) {
	t.Helper()
	var response *cliproxyexecutor.RequestTerminatedError
	if !errors.As(err, &response) || response != terminal {
		t.Fatalf("response = %v, want original terminal response", err)
	}
	var downstream interface {
		StatusCode() int
		ResponseHeaders() http.Header
		ResponseBody() []byte
	}
	if !errors.As(err, &downstream) || downstream.StatusCode() != terminal.HTTPStatus ||
		!reflect.DeepEqual(downstream.ResponseHeaders(), terminal.Header) || string(downstream.ResponseBody()) != string(terminal.Body) {
		t.Fatalf("downstream status, headers or body changed: %v", err)
	}
}
