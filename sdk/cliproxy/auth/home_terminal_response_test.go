package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestHomeTerminalResponseStopsCredentialFailover(t *testing.T) {
	for _, mode := range []string{"execute", "count_tokens", "stream", "stream_bootstrap"} {
		for _, wrapped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/wrapped=%t", mode, wrapped), func(t *testing.T) {
				terminal := &cliproxyexecutor.RequestTerminatedError{
					HTTPStatus: http.StatusBadGateway,
					Header: http.Header{
						"Content-Type": []string{"application/json"},
						"Retry-After":  []string{"7"},
					},
					Body: []byte(`{"detail":"request outcome is unknown"}`),
				}
				var failure error = terminal
				if wrapped {
					failure = fmt.Errorf("native response: %w", terminal)
				}
				dispatcher := &retryContractHomeDispatcher{authIDs: []string{"home-retry-a", "home-retry-b"}}
				executor := &retryContractHomeExecutor{failure: failure, streamBootstrap: mode == "stream_bootstrap"}
				registry := executionregistry.New()
				manager := NewManager(nil, nil, nil)
				manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
				manager.SetRetryConfig(2, time.Second, 2)
				manager.PublishHomeDispatch(dispatcher, registry, 1)
				manager.RegisterExecutor(executor)
				req := cliproxyexecutor.Request{Model: "gpt"}
				var errExecute error
				switch mode {
				case "execute":
					_, errExecute = manager.Execute(context.Background(), []string{executor.Identifier()}, req, cliproxyexecutor.Options{})
				case "count_tokens":
					_, errExecute = manager.ExecuteCount(context.Background(), []string{executor.Identifier()}, req, cliproxyexecutor.Options{})
				default:
					_, errExecute = manager.ExecuteStream(context.Background(), []string{executor.Identifier()}, req, cliproxyexecutor.Options{Stream: true})
				}
				if got := executor.Calls(); len(got) != 1 || got[0] != "home-retry-a" {
					t.Fatalf("executor calls = %v, want only the first credential", got)
				}
				var received *cliproxyexecutor.RequestTerminatedError
				if !errors.As(errExecute, &received) || received != terminal {
					t.Fatalf("error = %v, want original terminal response", errExecute)
				}
				if received.StatusCode() != http.StatusBadGateway || string(received.ResponseBody()) != string(terminal.Body) || received.ResponseHeaders().Get("Retry-After") != "7" {
					t.Fatalf("terminal response changed: %#v", received)
				}
				if got := dispatcher.Excluded(); len(got) != 1 {
					t.Fatalf("Home dispatches = %v, want one dispatch", got)
				}
				if got := registry.FreezeInFlight(time.Time{}).Executions; len(got) != 0 {
					t.Fatalf("in-flight executions after terminal response = %v", got)
				}
			})
		}
	}
}

func TestHomeTerminalResponseSurvivesUnacknowledgedRelease(t *testing.T) {
	for _, mode := range []string{"execute", "stream"} {
		t.Run(mode, func(t *testing.T) {
			terminal := &cliproxyexecutor.RequestTerminatedError{HTTPStatus: http.StatusBadGateway, Body: []byte(`{"detail":"request outcome is unknown"}`)}
			registry := executionregistry.New()
			var releases atomic.Int32
			// Home never acknowledges a release, so waiting for one times out.
			registry.SetReleaseSink(func(group executionregistry.ReleaseGroup, sequence int64) *executionregistry.ReleaseTicket {
				releases.Add(1)
				return executionregistry.NewReleaseTicket(group, sequence, make(chan struct{}))
			})
			dispatcher := &accountedHomeExecutionDispatcher{auths: []Auth{
				{ID: "home-retry-a", Provider: "home-retry-contract", Status: StatusActive},
				{ID: "home-retry-b", Provider: "home-retry-contract", Status: StatusActive},
			}}
			executor := &retryContractHomeExecutor{failure: terminal}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{
				Home:                  internalconfig.HomeConfig{Enabled: true},
				CredentialConcurrency: internalconfig.CredentialConcurrencyConfig{CPACancelBound: 10 * time.Millisecond},
			})
			manager.PublishHomeDispatch(dispatcher, registry, 1)
			manager.RegisterExecutor(executor)
			req := cliproxyexecutor.Request{Model: "gpt"}
			retryLimit := -1
			var errExecute error
			if mode == "execute" {
				_, errExecute = manager.executeHomeOnce(context.Background(), []string{"home-retry-contract"}, req, cliproxyexecutor.Options{}, false, 1, &retryLimit)
			} else {
				_, errExecute = manager.executeStreamMixedOnce(context.Background(), []string{"home-retry-contract"}, req, cliproxyexecutor.Options{Stream: true}, 1, &retryLimit, 0, 0)
			}
			if releases.Load() == 0 {
				t.Fatal("the attempt was never released")
			}
			var received *cliproxyexecutor.RequestTerminatedError
			if !errors.As(errExecute, &received) || received != terminal {
				t.Fatalf("error = %T %v, want original terminal response", errExecute, errExecute)
			}
			if got := executor.Calls(); len(got) != 1 {
				t.Fatalf("executor calls = %v, want one", got)
			}
		})
	}
}

// credentialScopedTerminalError is a terminal response that one credential's quota caused, like
// Claude Fast's authoritative unified-quota 429.
type credentialScopedTerminalError struct {
	response *cliproxyexecutor.RequestTerminatedError
}

func (e *credentialScopedTerminalError) Error() string            { return "credential rejected the request" }
func (e *credentialScopedTerminalError) Unwrap() error            { return e.response }
func (e *credentialScopedTerminalError) IsCredentialScoped() bool { return true }

func TestHomeCredentialScopedTerminalResponseFailsOver(t *testing.T) {
	for _, mode := range []string{"execute", "count_tokens", "stream", "stream_bootstrap"} {
		t.Run(mode, func(t *testing.T) {
			failure := &credentialScopedTerminalError{response: &cliproxyexecutor.RequestTerminatedError{
				HTTPStatus: http.StatusTooManyRequests,
				Body:       []byte(`{"type":"error","error":{"type":"rate_limit_error"}}`),
			}}
			dispatcher := &retryContractHomeDispatcher{authIDs: []string{"home-retry-a", "home-retry-b"}}
			executor := &retryContractHomeExecutor{failure: failure, streamBootstrap: mode == "stream_bootstrap"}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.SetRetryConfig(2, time.Second, 2)
			manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
			manager.RegisterExecutor(executor)
			req := cliproxyexecutor.Request{Model: "gpt"}
			var errExecute error
			switch mode {
			case "execute":
				_, errExecute = manager.Execute(context.Background(), []string{executor.Identifier()}, req, cliproxyexecutor.Options{})
			case "count_tokens":
				_, errExecute = manager.ExecuteCount(context.Background(), []string{executor.Identifier()}, req, cliproxyexecutor.Options{})
			default:
				_, errExecute = manager.ExecuteStream(context.Background(), []string{executor.Identifier()}, req, cliproxyexecutor.Options{Stream: true})
			}
			if errExecute != nil {
				t.Fatalf("error = %v, want the second credential's success", errExecute)
			}
			if got := executor.Calls(); len(got) != 2 || got[0] != "home-retry-a" || got[1] != "home-retry-b" {
				t.Fatalf("executor calls = %v, want failover to the second credential", got)
			}
		})
	}
}

func TestHomeTerminalResponseHonorsConfiguredContinue(t *testing.T) {
	for _, action := range []string{RequestScopedActionContinue, RequestScopedActionContinueAndCooldown} {
		for _, mode := range []string{"execute", "count_tokens", "stream"} {
			t.Run(action+"/"+mode, func(t *testing.T) {
				failure := &cliproxyexecutor.RequestTerminatedError{
					HTTPStatus: http.StatusBadRequest,
					Body:       []byte(`{"error":"context_length_exceeded"}`),
				}
				dispatcher := &retryContractHomeDispatcher{
					authIDs: []string{"home-retry-a", "home-retry-b"},
					metadata: map[string]any{"request_scoped_errors": []internalconfig.RequestScopedErrorRule{
						{Status: http.StatusBadRequest, Match: []string{"context_length_exceeded"}, Action: action},
					}},
				}
				executor := &retryContractHomeExecutor{failure: failure}
				manager := NewManager(nil, nil, nil)
				manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
				manager.SetRetryConfig(2, time.Second, 2)
				manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
				manager.RegisterExecutor(executor)
				req := cliproxyexecutor.Request{Model: "gpt"}
				var errExecute error
				switch mode {
				case "execute":
					_, errExecute = manager.Execute(context.Background(), []string{executor.Identifier()}, req, cliproxyexecutor.Options{})
				case "count_tokens":
					_, errExecute = manager.ExecuteCount(context.Background(), []string{executor.Identifier()}, req, cliproxyexecutor.Options{})
				default:
					_, errExecute = manager.ExecuteStream(context.Background(), []string{executor.Identifier()}, req, cliproxyexecutor.Options{Stream: true})
				}
				if errExecute != nil {
					t.Fatalf("error = %v, want the second credential's success", errExecute)
				}
				if got := executor.Calls(); len(got) != 2 || got[0] != "home-retry-a" || got[1] != "home-retry-b" {
					t.Fatalf("executor calls = %v, want the configured continue to reach the second credential", got)
				}
			})
		}
	}
}
