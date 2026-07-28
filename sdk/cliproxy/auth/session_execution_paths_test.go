package auth

import (
	"context"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// sessionCapturingExecutor records the session each execution path puts on the
// executor context, which is where usage reporters read it from.
type sessionCapturingExecutor struct {
	homeExecutionExecutor
	seen coreusage.SessionInfo
}

func (e *sessionCapturingExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.seen = coreusage.SessionInfoFromContext(ctx)
	return e.homeExecutionExecutor.Execute(ctx, auth, req, opts)
}

func (e *sessionCapturingExecutor) CountTokens(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.seen = coreusage.SessionInfoFromContext(ctx)
	return cliproxyexecutor.Response{}, nil
}

func (e *sessionCapturingExecutor) ExecuteStream(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.seen = coreusage.SessionInfoFromContext(ctx)
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ok")}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

// Home dispatch builds its own attempt context. It has to publish the routed
// session too, or usage records from Home deployments carry no session at all.
func TestHomeExecutionPublishesSessionToExecutorContext(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Manager, cliproxyexecutor.Options) error
	}{
		{
			name: "execute",
			run: func(manager *Manager, opts cliproxyexecutor.Options) error {
				_, err := manager.Execute(context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-a"}, opts)
				return err
			},
		},
		{
			name: "count_tokens",
			run: func(manager *Manager, opts cliproxyexecutor.Options) error {
				_, err := manager.ExecuteCount(context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-a"}, opts)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.PublishHomeDispatch(homeExecutionDispatcher{}, executionregistry.New(), 1)
			executor := &sessionCapturingExecutor{}
			manager.RegisterExecutor(executor)

			opts := cliproxyexecutor.Options{Headers: http.Header{
				"X-Session-Id": {"ses_home"},
				"User-Agent":   {"opencode/1.18.3"},
			}}
			if err := tt.run(manager, opts); err != nil {
				t.Fatalf("%s error = %v", tt.name, err)
			}

			if executor.seen.ID != "header:ses_home" {
				t.Fatalf("session ID on the executor context = %q, want header:ses_home", executor.seen.ID)
			}
			if executor.seen.ClientType != "opencode" {
				t.Fatalf("client type = %q, want opencode", executor.seen.ClientType)
			}
		})
	}
}

func TestHomeExecutionUsageReusesCanonicalDispatchSession(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(homeExecutionDispatcher{}, executionregistry.New(), 1)
	executor := &sessionCapturingExecutor{}
	manager.RegisterExecutor(executor)

	combined := cliproxyexecutor.Options{OriginalRequest: []byte(`{"prompt_cache_key":"cache-1","conversation":{"id":"conv-1"}}`)}
	if _, errExecute := manager.Execute(context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-a"}, combined); errExecute != nil {
		t.Fatalf("combined Execute() error = %v", errExecute)
	}
	if executor.seen.ID != "pck:cache-1" {
		t.Fatalf("combined usage session ID = %q, want pck:cache-1", executor.seen.ID)
	}

	conversationOnly := cliproxyexecutor.Options{OriginalRequest: []byte(`{"conversation":{"id":"conv-1"}}`)}
	if _, errExecute := manager.Execute(context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-a"}, conversationOnly); errExecute != nil {
		t.Fatalf("conversation-only Execute() error = %v", errExecute)
	}
	if executor.seen.ID != "pck:cache-1" {
		t.Fatalf("conversation-only usage session ID = %q, want routed pck:cache-1", executor.seen.ID)
	}
}

func TestHomeStreamUsageReusesCanonicalDispatchSession(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(homeExecutionDispatcher{}, executionregistry.New(), 1)
	executor := &sessionCapturingExecutor{}
	manager.RegisterExecutor(executor)

	execute := func(opts cliproxyexecutor.Options) {
		t.Helper()
		result, errStream := manager.ExecuteStream(context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-a"}, opts)
		if errStream != nil {
			t.Fatalf("ExecuteStream() error = %v", errStream)
		}
		for range result.Chunks {
		}
	}

	execute(cliproxyexecutor.Options{OriginalRequest: []byte(`{"prompt_cache_key":"cache-1","conversation":{"id":"conv-1"}}`)})
	if executor.seen.ID != "pck:cache-1" {
		t.Fatalf("combined stream usage session ID = %q, want pck:cache-1", executor.seen.ID)
	}
	execute(cliproxyexecutor.Options{OriginalRequest: []byte(`{"conversation":{"id":"conv-1"}}`)})
	if executor.seen.ID != "pck:cache-1" {
		t.Fatalf("conversation-only stream usage session ID = %q, want routed pck:cache-1", executor.seen.ID)
	}
}
