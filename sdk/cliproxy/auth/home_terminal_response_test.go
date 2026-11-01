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
