package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type drainTestExecutor struct {
	streamFunc func(ctx context.Context, req cliproxyexecutor.Request) (*cliproxyexecutor.StreamResult, error)
}

func (e *drainTestExecutor) Identifier() string { return "test-drain-provider" }

func (e *drainTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *drainTestExecutor) ExecuteStream(ctx context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return e.streamFunc(ctx, req)
}

func (e *drainTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *drainTestExecutor) Refresh(_ context.Context, a *Auth) (*Auth, error) { return a, nil }

func (e *drainTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestConductor_ExecuteStreamDrainsSourceOnTerminalEmpty_SingleModel(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-drain-empty-single", Provider: "test-drain-provider", Status: StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "test-drain-provider", []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	manager.RefreshSchedulerEntry(auth.ID)

	producerDone := make(chan struct{})
	exec := &drainTestExecutor{
		streamFunc: func(ctx context.Context, req cliproxyexecutor.Request) (*cliproxyexecutor.StreamResult, error) {
			chunks := make(chan cliproxyexecutor.StreamChunk)
			go func() {
				defer close(producerDone)
				// Terminal empty marker (OpenAI [DONE])
				chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: [DONE]\n\n")}
				// Trailing chunk on unbuffered channel - will block if chunks not drained
				chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("trailing chunk")}
				close(chunks)
			}()
			return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
		},
	}
	manager.RegisterExecutor(exec)

	res, err := manager.ExecuteStream(context.Background(), []string{"test-drain-provider"}, cliproxyexecutor.Request{Model: "test-model"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream unexpected error: %v", err)
	}
	if res == nil || res.Chunks == nil {
		t.Fatal("expected non-nil StreamResult with Chunks")
	}
	var receivedErr error
	for chunk := range res.Chunks {
		if chunk.Err != nil {
			receivedErr = chunk.Err
		}
	}
	if receivedErr == nil {
		t.Fatal("expected empty completion error on Chunks, got nil")
	}

	select {
	case <-producerDone:
		// PASS: producer unblocked because streamResult.Chunks was drained
	case <-time.After(500 * time.Millisecond):
		t.Fatal("producer remained blocked after terminal empty error; source streamResult.Chunks was not drained")
	}
}

func TestConductor_ExecuteStreamDrainsSourceOnTerminalEmpty_ModelPoolFailover(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-drain-empty-pool", Provider: "test-drain-provider", Status: StatusActive}

	model1ProducerDone := make(chan struct{})
	model2ProducerDone := make(chan struct{})

	exec := &drainTestExecutor{
		streamFunc: func(ctx context.Context, req cliproxyexecutor.Request) (*cliproxyexecutor.StreamResult, error) {
			chunks := make(chan cliproxyexecutor.StreamChunk)
			if req.Model == "model-1" {
				go func() {
					defer close(model1ProducerDone)
					// Model 1 returns terminal empty and then attempts trailing chunk
					chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: [DONE]\n\n")}
					chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("trailing chunk 1")}
					close(chunks)
				}()
			} else {
				go func() {
					defer close(model2ProducerDone)
					// Model 2 returns valid streaming content
					chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")}
					close(chunks)
				}()
			}
			return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
		},
	}

	res, err := manager.executeStreamWithModelPool(
		context.Background(),
		exec,
		auth,
		"test-drain-provider",
		cliproxyexecutor.Request{Model: "pool-model"},
		cliproxyexecutor.Options{},
		"pool-model",
		"",
		[]string{"model-1", "model-2"},
		true,
		OAuthModelAliasResult{},
		nil,
		true,
		false,
		nil,
	)
	if err != nil {
		t.Fatalf("executeStreamWithModelPool unexpected error: %v", err)
	}
	if res == nil || res.Chunks == nil {
		t.Fatal("expected non-nil StreamResult with Chunks")
	}
	for range res.Chunks {
	}

	select {
	case <-model1ProducerDone:
		// PASS: model-1 producer unblocked because discarded before failover
	case <-time.After(500 * time.Millisecond):
		t.Fatal("model-1 producer remained blocked after model failover; source was not drained")
	}

	select {
	case <-model2ProducerDone:
		// PASS: model-2 completed normally
	case <-time.After(500 * time.Millisecond):
		t.Fatal("model-2 producer did not complete")
	}
}
