package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestHomeSchedulerSelectionIdentityReachesEveryExecutionMode(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream"} {
		t.Run(mode, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.PublishHomeDispatch(&retryContractHomeDispatcher{authIDs: []string{"home-auth"}, selectionID: "home-selection"}, executionregistry.New(), 1)
			seen := false
			observe := func(ctx context.Context, opts cliproxyexecutor.Options) {
				seen = true
				if coreusage.SelectionIDFromContext(ctx) != "home-selection" || opts.Metadata[cliproxyexecutor.SelectionIDMetadataKey] != "home-selection" {
					t.Fatalf("selection identity lost: context=%q metadata=%v", coreusage.SelectionIDFromContext(ctx), opts.Metadata)
				}
			}
			manager.RegisterExecutor(&callbackSchedulerTestExecutor{
				schedulerTestExecutor: schedulerTestExecutor{provider: "home-retry-contract"},
				execute: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					observe(ctx, opts)
					return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
				},
				countTokens: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					observe(ctx, opts)
					return cliproxyexecutor.Response{Payload: []byte(`{"input_tokens":1}`)}, nil
				},
				executeStream: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					observe(ctx, opts)
					chunks := make(chan cliproxyexecutor.StreamChunk, 1)
					chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {}\n\n")}
					close(chunks)
					return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
				},
			})
			ctx := coreusage.WithSelectionID(t.Context(), "stale-context")
			opts := cliproxyexecutor.Options{Stream: mode == "stream", Metadata: map[string]any{cliproxyexecutor.SelectionIDMetadataKey: "stale-metadata"}}
			req := cliproxyexecutor.Request{Model: "gpt"}
			var errExecute error
			switch mode {
			case "execute":
				_, errExecute = manager.Execute(ctx, []string{"home-retry-contract"}, req, opts)
			case "count":
				_, errExecute = manager.ExecuteCount(ctx, []string{"home-retry-contract"}, req, opts)
			case "stream":
				var result *cliproxyexecutor.StreamResult
				result, errExecute = manager.ExecuteStream(ctx, []string{"home-retry-contract"}, req, opts)
				if errExecute == nil {
					for range result.Chunks {
					}
				}
			}
			if errExecute != nil || !seen {
				t.Fatalf("seen=%t error=%v", seen, errExecute)
			}
		})
	}
}

type homeContinuityDispatcher struct {
	protocol int
	picks    int
	queries  []home.SchedulerContinuityQuery
	pick     func(int) ([]byte, error)
	evaluate func(int) (pluginapi.SchedulerContinuityResponse, bool, error)
}

func (*homeContinuityDispatcher) HeartbeatOK() bool        { return true }
func (*homeContinuityDispatcher) AbortAmbiguousDispatch()  {}
func (d *homeContinuityDispatcher) SchedulerProtocol() int { return d.protocol }
func (d *homeContinuityDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	d.picks++
	return d.pick(d.picks)
}
func (d *homeContinuityDispatcher) EvaluateSchedulerContinuity(_ context.Context, query home.SchedulerContinuityQuery) (pluginapi.SchedulerContinuityResponse, bool, error) {
	d.queries = append(d.queries, query)
	return d.evaluate(len(d.queries))
}

func TestHomeContinuityUsesRemoteCandidatesAndSharedBudget(t *testing.T) {
	for _, mode := range []string{"execute", "stream"} {
		for _, phase := range []string{"pre_admission", "pre_output_429"} {
			t.Run(mode+"/"+phase, func(t *testing.T) {
				clock := newFakeContinuityClock(time.Unix(100, 0))
				dispatcher := &homeContinuityDispatcher{protocol: 1}
				dispatcher.pick = func(n int) ([]byte, error) {
					if phase == "pre_admission" && n == 1 {
						return []byte(`{"error":{"type":"model_cooldown","message":"quota cooldown","retry_after_ms":60000}}`), nil
					}
					if phase == "pre_output_429" && n == 2 {
						return nil, home.ErrAuthNotFound
					}
					limit := 1
					return json.Marshal(homeAuthDispatchResponse{SelectionID: "remote-selection", RequestRetry: &limit, Auth: Auth{ID: "remote-codex", Provider: "codex", Status: StatusActive}})
				}
				dispatcher.evaluate = func(n int) (pluginapi.SchedulerContinuityResponse, bool, error) {
					decision := pluginapi.SchedulerContinuityWait
					if n > 1 {
						decision = pluginapi.SchedulerContinuityReady
					}
					return pluginapi.SchedulerContinuityResponse{Decision: decision, PollIntervalMs: 10, WaitBudgetMs: 100}, true, nil
				}
				manager := NewManager(nil, nil, nil)
				manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}, RequestRetry: 1})
				manager.SetContinuityClockForTest(clock)
				manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
				attempts := 0
				execute := func(ctx context.Context) error {
					attempts++
					cliproxyexecutor.MarkUpstreamAttempt(ctx)
					if phase == "pre_output_429" && attempts == 1 {
						return &Error{Code: "quota_exceeded", Message: "quota exhausted", HTTPStatus: 429}
					}
					return nil
				}
				manager.RegisterExecutor(&continuityTestExecutor{provider: "codex",
					execFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
						return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, execute(ctx)
					},
					streamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
						if errExecute := execute(ctx); errExecute != nil {
							return nil, errExecute
						}
						chunks := make(chan cliproxyexecutor.StreamChunk, 1)
						chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {}\n\n")}
						close(chunks)
						return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
					},
				})
				opts := cliproxyexecutor.Options{Stream: mode == "stream", Headers: http.Header{"X-Api-Key": {"client-key"}}}
				var errExecute error
				if mode == "stream" {
					var result *cliproxyexecutor.StreamResult
					result, errExecute = manager.ExecuteStream(t.Context(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt"}, opts)
					if errExecute == nil {
						for range result.Chunks {
						}
					}
				} else {
					_, errExecute = manager.Execute(t.Context(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt"}, opts)
				}
				if errExecute != nil || len(dispatcher.queries) != 2 || clock.Now().Sub(time.Unix(100, 0)) != 10*time.Millisecond {
					t.Fatalf("error=%v queries=%+v elapsed=%s", errExecute, dispatcher.queries, clock.Now().Sub(time.Unix(100, 0)))
				}
				for _, query := range dispatcher.queries {
					if query.Request.Phase != phase || query.Request.Stream != opts.Stream || query.Request.RequestID == "" || len(query.Request.Candidates) != 0 {
						t.Fatalf("invalid observation: %+v", query)
					}
				}
			})
		}
	}
}

func TestHomeContinuityDisabledStopsAndEmptyObservationPreservesOrdinaryRetries(t *testing.T) {
	dispatcher := &homeContinuityDispatcher{protocol: 1}
	dispatcher.pick = func(int) ([]byte, error) {
		return []byte(`{"error":{"type":"model_cooldown","message":"quota cooldown","retry_after_ms":60000}}`), nil
	}
	dispatcher.evaluate = func(int) (pluginapi.SchedulerContinuityResponse, bool, error) {
		return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityDisabled}, true, nil
	}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	_, errExecute := manager.Execute(t.Context(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{})
	if !IsContinuityTerminatedError(errExecute) || dispatcher.picks != 1 || len(dispatcher.queries) != 1 {
		t.Fatalf("error=%v picks=%d observations=%d, want immediate continuity termination", errExecute, dispatcher.picks, len(dispatcher.queries))
	}
	dispatcher.evaluate = func(int) (pluginapi.SchedulerContinuityResponse, bool, error) {
		return pluginapi.SchedulerContinuityResponse{}, false, nil
	}
	session := &continuitySession{}
	outcome, errGate := manager.runCodexContinuityGate(t.Context(), session, "pre_admission", []string{"codex"}, "gpt", cliproxyexecutor.Options{}, 0, false, 0)
	if errGate != nil || outcome != continuityOutcomeDisabled || session.terminated {
		t.Fatalf("outcome=%v error=%v session=%+v, want ordinary retry path preserved", outcome, errGate, session)
	}
}

func TestHomeContinuityBudgetCannotExtendAndErrorsStayTerminal(t *testing.T) {
	clock := newFakeContinuityClock(time.Unix(100, 0))
	dispatcher := &homeContinuityDispatcher{protocol: 1}
	dispatcher.evaluate = func(n int) (pluginapi.SchedulerContinuityResponse, bool, error) {
		budget := int64(20)
		if n > 1 {
			budget = 100_000
		}
		return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityWait, PollIntervalMs: 10, WaitBudgetMs: budget}, true, nil
	}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	manager.SetContinuityClockForTest(clock)
	session := &continuitySession{}
	outcome, errGate := manager.runCodexContinuityGate(t.Context(), session, "pre_admission", []string{"codex"}, "gpt", cliproxyexecutor.Options{}, 0, false, 0)
	if errGate != nil || outcome != continuityOutcomeDisabled || !session.budgetExhausted || clock.Now().Sub(time.Unix(100, 0)) != 20*time.Millisecond {
		t.Fatalf("outcome=%v session=%+v error=%v", outcome, session, errGate)
	}
	failure := errors.New("remote evaluation failed")
	dispatcher.evaluate = func(int) (pluginapi.SchedulerContinuityResponse, bool, error) {
		return pluginapi.SchedulerContinuityResponse{}, false, failure
	}
	_, errGate = manager.runCodexContinuityGate(t.Context(), &continuitySession{}, "pre_admission", []string{"codex"}, "gpt", cliproxyexecutor.Options{}, 0, false, 0)
	if !errors.Is(errGate, failure) || !isRequestStopError(errGate) {
		t.Fatalf("error=%v, want terminal remote failure", errGate)
	}
}

func TestHomeSchedulerSelectionIdentitySurvivesRetainedExecutions(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			dispatcher := &homeContinuityDispatcher{}
			dispatcher.pick = func(n int) ([]byte, error) {
				return json.Marshal(homeAuthDispatchResponse{SelectionID: fmt.Sprintf("selection-%d", n), Auth: Auth{ID: "retained-auth", Provider: "home-retry-contract", Status: StatusActive, Attributes: map[string]string{"websockets": "true"}}})
			}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
			t.Cleanup(func() { manager.CloseExecutionSession("identity-session") })
			var ids []string
			observe := func(ctx context.Context, opts cliproxyexecutor.Options) {
				id := coreusage.SelectionIDFromContext(ctx)
				if id == "" || opts.Metadata[cliproxyexecutor.SelectionIDMetadataKey] != id {
					t.Fatalf("identity=%q metadata=%v", id, opts.Metadata)
				}
				ids = append(ids, id)
				opts.ExecutionLifecycle.(interface{ Retain() }).Retain()
			}
			manager.RegisterExecutor(&callbackSchedulerTestExecutor{
				schedulerTestExecutor: schedulerTestExecutor{provider: "home-retry-contract"},
				execute: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					observe(ctx, opts)
					return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
				},
				executeStream: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					observe(ctx, opts)
					chunks := make(chan cliproxyexecutor.StreamChunk, 1)
					chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {}\n\n")}
					close(chunks)
					return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
				},
			})
			ctx := cliproxyexecutor.WithDownstreamWebsocket(t.Context())
			opts := cliproxyexecutor.Options{Stream: stream, Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "identity-session"}}
			for _, model := range []string{"gpt", "gpt", "other"} {
				if stream {
					result, errExecute := manager.ExecuteStream(ctx, []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: model}, opts)
					if errExecute != nil {
						t.Fatal(errExecute)
					}
					for range result.Chunks {
					}
				} else {
					if _, errExecute := manager.Execute(ctx, []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: model}, opts); errExecute != nil {
						t.Fatal(errExecute)
					}
				}
			}
			if dispatcher.picks != 2 || len(ids) != 3 || ids[0] != "selection-1" || ids[1] != ids[0] || ids[2] != "selection-2" {
				t.Fatalf("picks=%d identities=%v", dispatcher.picks, ids)
			}
		})
	}
}

func TestHomeContinuityPreservesStructuredAccessErrors(t *testing.T) {
	for _, payload := range []string{`{"error":{"type":"invalid_credential","message":"invalid key"}}`, `{}`} {
		manager := NewManager(nil, nil, nil)
		manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
		dispatcher := &homeContinuityDispatcher{protocol: 1, evaluate: func(int) (pluginapi.SchedulerContinuityResponse, bool, error) {
			return pluginapi.SchedulerContinuityResponse{}, false, &home.SchedulerContinuityError{Payload: []byte(payload)}
		}}
		manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
		_, errGate := manager.runCodexContinuityGate(t.Context(), &continuitySession{}, "pre_admission", []string{"codex"}, "gpt", cliproxyexecutor.Options{}, 0, false, 0)
		var classified *Error
		wantCode, wantStatus := "invalid_credential", http.StatusUnauthorized
		if payload == `{}` {
			wantCode, wantStatus = "invalid_auth", http.StatusBadGateway
		}
		if !isRequestStopError(errGate) || !errors.As(errGate, &classified) || classified.Code != wantCode || classified.HTTPStatus != wantStatus {
			t.Fatalf("error=%v, want %s/%d", errGate, wantCode, wantStatus)
		}
	}
}

func TestHomeContinuityDoesNotWaitForTokenCount(t *testing.T) {
	dispatcher := &homeContinuityDispatcher{protocol: 1, pick: func(int) ([]byte, error) {
		return []byte(`{"error":{"type":"model_cooldown","message":"quota","retry_after_ms":60000}}`), nil
	}, evaluate: func(int) (pluginapi.SchedulerContinuityResponse, bool, error) {
		t.Fatal("token count entered continuity")
		return pluginapi.SchedulerContinuityResponse{}, false, nil
	}}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetRetryConfig(0, 0, 0)
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	if _, errExecute := manager.ExecuteCount(t.Context(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{}); errExecute == nil {
		t.Fatal("missing cooldown error")
	}
	if len(dispatcher.queries) != 0 || dispatcher.picks != 1 {
		t.Fatalf("queries=%d picks=%d", len(dispatcher.queries), dispatcher.picks)
	}
}
