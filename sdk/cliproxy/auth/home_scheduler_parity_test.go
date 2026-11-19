package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/MoeForge/moego/json"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestHomeSchedulerRejectMatchesLocalRetryRounds(t *testing.T) {
	previous := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(-1)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(previous) })
	for _, mode := range []string{"execute", "count", "stream"} {
		for _, initial := range []bool{false, true} {
			for _, retry := range []int{0, 1} {
				t.Run(fmt.Sprintf("%s/initial=%t/retry=%d", mode, initial, retry), func(t *testing.T) {
					type outcome struct {
						picks, attempts int
						code            string
						status          int
					}
					outcomes := make([]outcome, 0, 2)
					for _, remote := range []bool{false, true} {
						result := outcome{}
						manager := NewManager(nil, &FillFirstSelector{}, nil)
						manager.SetRetryConfig(retry, 0, 0)
						reject := func() bool { result.picks++; return initial || result.picks%2 == 0 }
						if remote {
							manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
							dispatcher := &homeContinuityDispatcher{}
							dispatcher.pick = func(int) ([]byte, error) {
								if reject() {
									return []byte(`{"error":{"type":"scheduler_rejected","code":"policy_denied","message":"policy denied"}}`), nil
								}
								return json.Marshal(homeAuthDispatchResponse{RequestRetry: &retry, Auth: Auth{ID: "parity-a", Provider: "home-retry-contract", Status: StatusActive}})
							}
							manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
						} else {
							for _, id := range []string{"parity-a", "parity-b"} {
								registry.GetGlobalRegistry().RegisterClient(id, "home-retry-contract", []*registry.ModelInfo{{ID: "gpt"}})
								t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
								if _, errRegister := manager.Register(t.Context(), &Auth{ID: id, Provider: "home-retry-contract", Status: StatusActive}); errRegister != nil {
									t.Fatal(errRegister)
								}
							}
							manager.SetPluginScheduler(&fakePluginScheduler{pick: func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
								if reject() {
									return pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: "policy_denied", RejectReason: "policy denied"}, true, nil
								}
								return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "parity-a"}, true, nil
							}})
						}
						execute := func(ctx context.Context) error {
							cliproxyexecutor.MarkUpstreamAttempt(ctx)
							result.attempts++
							if result.attempts == 1 {
								return &Error{Code: "upstream_error", Message: "upstream failed", HTTPStatus: http.StatusInternalServerError, Retryable: true}
							}
							return nil
						}
						manager.RegisterExecutor(&callbackSchedulerTestExecutor{
							schedulerTestExecutor: schedulerTestExecutor{provider: "home-retry-contract"},
							execute: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
								return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, execute(ctx)
							},
							countTokens: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
								return cliproxyexecutor.Response{Payload: []byte(`{"input_tokens":1}`)}, execute(ctx)
							},
							executeStream: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
								if errExecute := execute(ctx); errExecute != nil {
									return nil, errExecute
								}
								chunks := make(chan cliproxyexecutor.StreamChunk, 1)
								chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {}\n\n")}
								close(chunks)
								return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
							},
						})
						var errExecute error
						req := cliproxyexecutor.Request{Model: "gpt"}
						switch mode {
						case "execute":
							_, errExecute = manager.Execute(t.Context(), []string{"home-retry-contract"}, req, cliproxyexecutor.Options{})
						case "count":
							_, errExecute = manager.ExecuteCount(t.Context(), []string{"home-retry-contract"}, req, cliproxyexecutor.Options{})
						case "stream":
							var response *cliproxyexecutor.StreamResult
							response, errExecute = manager.ExecuteStream(t.Context(), []string{"home-retry-contract"}, req, cliproxyexecutor.Options{Stream: true})
							if errExecute == nil {
								for chunk := range response.Chunks {
									if chunk.Err != nil {
										errExecute = chunk.Err
									}
								}
							}
						}
						var authErr *Error
						if errors.As(errExecute, &authErr) {
							result.code = authErr.Code
							result.status = authErr.HTTPStatus
						} else if errExecute != nil {
							t.Fatalf("unexpected error: %v", errExecute)
						}
						outcomes = append(outcomes, result)
					}
					if outcomes[0] != outcomes[1] {
						t.Fatalf("local=%+v Home=%+v", outcomes[0], outcomes[1])
					}
					want := outcome{picks: 3, attempts: 2}
					if initial {
						want = outcome{picks: 1, code: "policy_denied"}
					} else if retry == 0 {
						want = outcome{picks: 2, attempts: 1, code: "upstream_error", status: 500}
					}
					if outcomes[1] != want {
						t.Fatalf("outcome=%+v, want %+v", outcomes[1], want)
					}
				})
			}
		}
	}
}
