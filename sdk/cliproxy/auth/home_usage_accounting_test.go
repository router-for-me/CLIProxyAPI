package auth

import (
	"context"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

type homeUsageAccountingExecutor struct {
	retainingHomeExecutionExecutor
	observed []*coreusage.HomeSelection
}

func (e *homeUsageAccountingExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.observed = append(e.observed, coreusage.HomeSelectionFromContext(ctx))
	return e.retainingHomeExecutionExecutor.Execute(ctx, auth, req, opts)
}

func (e *homeUsageAccountingExecutor) ExecuteStream(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.observed = append(e.observed, coreusage.HomeSelectionFromContext(ctx))
	if lifecycle, ok := opts.ExecutionLifecycle.(interface{ Retain() }); ok {
		lifecycle.Retain()
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed","response":{"id":"test"}}`)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func TestHomeUsageMarksOnlyReusedExecutions(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "execute", true: "stream"}[stream], func(t *testing.T) {
			dispatcher := &retainingHomeExecutionDispatcher{}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
			executor := &homeUsageAccountingExecutor{}
			manager.RegisterExecutor(executor)
			t.Cleanup(func() { manager.CloseExecutionSession("usage-session") })
			ctx := cliproxyexecutor.WithDownstreamWebsocket(t.Context())
			opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "usage-session"}}
			for _, model := range []string{"model-a", "model-a", "model-a", "model-b"} {
				if stream {
					result, errExecute := manager.ExecuteStream(ctx, []string{"home-execution"}, cliproxyexecutor.Request{Model: model}, opts)
					if errExecute != nil {
						t.Fatal(errExecute)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, errExecute := manager.Execute(ctx, []string{"home-execution"}, cliproxyexecutor.Request{Model: model}, opts); errExecute != nil {
					t.Fatal(errExecute)
				}
			}
			if dispatcher.calls.Load() != 2 || len(executor.observed) != 4 {
				t.Fatalf("dispatch=%d executions=%d", dispatcher.calls.Load(), len(executor.observed))
			}
			for i, want := range []bool{false, true, true, false} {
				got := executor.observed[i]
				wantModel := "model-a"
				if i == 3 {
					wantModel = "model-b"
				}
				if got == nil || got.Reused != want || got.Model != wantModel {
					t.Fatalf("turn %d: got=%+v want reused=%t model=%s", i, got, want, wantModel)
				}
			}
		})
	}
}
