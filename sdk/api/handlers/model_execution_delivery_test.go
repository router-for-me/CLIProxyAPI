package handlers

import (
	"context"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestInternalModelExecutionIsolatesDelivery(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "nonstream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			outer, finish := usage.WithStreamDelivery(parent)
			defer finish(context.Canceled)
			seen := make(chan context.Context, 1)
			executor := &modelExecutionCaptureExecutor{
				execute: func(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
					seen <- ctx
					return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
				},
				stream: func(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
					seen <- ctx
					chunks := make(chan coreexecutor.StreamChunk, 1)
					chunks <- coreexecutor.StreamChunk{Payload: []byte("data: {\"ok\":true}\n\n")}
					close(chunks)
					return &coreexecutor.StreamResult{Chunks: chunks}, nil
				},
			}
			h := newModelExecutionHandler(t, "nested-delivery-model", executor, &sdkconfig.SDKConfig{})
			req := ModelExecutionRequest{Model: "nested-delivery-model", EntryProtocol: "openai", Stream: stream, Body: []byte(`{"model":"nested-delivery-model"}`)}
			if stream {
				result, errMsg := h.ExecuteModelStream(outer, req)
				if errMsg != nil {
					t.Fatal(errMsg)
				}
				for range result.Chunks {
				}
			} else {
				if _, errMsg := h.ExecuteModel(outer, req); errMsg != nil {
					t.Fatal(errMsg)
				}
			}
			ctx := <-seen
			if usage.StreamDeliveryTracked(ctx) {
				t.Fatal("internal execution inherited HTTP acknowledgment")
			}
			stop := usage.SupportStreamDelivery(ctx)
			defer stop()
			if usage.StreamDeliverySupported(outer) {
				t.Error("nested support escaped to HTTP consumer")
			}
			cancel()
			if ctx.Err() != context.Canceled {
				t.Error("internal execution lost caller cancellation")
			}
		})
	}
}
