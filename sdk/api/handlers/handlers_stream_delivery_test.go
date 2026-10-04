package handlers

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestIssue6332LifecycleDeliveryReadyBoundary(t *testing.T) {
	for _, tc := range []struct {
		name        string
		deliveryErr error
		upstreamErr error
		want        pluginapi.RequestCompletionOutcome
		status      int
	}{
		{name: "delivered", want: pluginapi.RequestCompletionSucceeded, status: http.StatusOK},
		{name: "early_cancel", deliveryErr: context.Canceled, want: pluginapi.RequestCompletionCanceled},
		{name: "write_failure", deliveryErr: io.ErrClosedPipe, want: pluginapi.RequestCompletionFailed, status: http.StatusInternalServerError},
		{name: "read_failure", upstreamErr: io.ErrUnexpectedEOF, want: pluginapi.RequestCompletionFailed, status: http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for range 100 {
				ctx, finish := coreusage.WithStreamDelivery(context.Background())
				ctx, cancel := context.WithCancel(ctx)
				finish(tc.deliveryErr)
				cancel()
				chunks := make(chan coreexecutor.StreamChunk)
				close(chunks)
				// Both EOF and cancellation are ready before the shared select.
				_, _, canceled := nextStreamChunk(ctx, nil, nil, chunks)
				outcome, status, err := pluginapi.RequestCompletionSucceeded, http.StatusOK, error(nil)
				if canceled {
					outcome, status, err = pluginapi.RequestCompletionCanceled, 0, ctx.Err()
				}
				if tc.upstreamErr != nil {
					outcome, status, err = pluginapi.RequestCompletionFailed, http.StatusBadGateway, tc.upstreamErr
				}
				outcome, status, err = streamDeliveryCompletion(ctx, outcome, status, err)
				completions := make(chan pluginapi.RequestCompletion, 1)
				h := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
				h.SetPluginHost(&handlerInterceptorTestHost{completeRequest: func(_ context.Context, completion pluginapi.RequestCompletion) { completions <- completion }})
				h.newRequestLifecycleTracker(ctx, "openai-response", "model", "model", true, nil, "").complete(outcome, status, err)
				completion := <-completions
				wantErr := tc.deliveryErr
				if tc.upstreamErr != nil {
					wantErr = tc.upstreamErr
				}
				wantMessage := ""
				if wantErr != nil {
					wantMessage = wantErr.Error()
				}
				if completion.Outcome != tc.want || completion.StatusCode != tc.status || completion.Error != wantMessage {
					t.Fatalf("completion = %+v, want %s/%d/%q", completion, tc.want, tc.status, wantMessage)
				}
			}
		})
	}
}

func TestIssue6332LifecycleClosesChannelsBeforeDeliveryWait(t *testing.T) {
	model := "issue6332-lifecycle-eof"
	executor := &interceptorCaptureExecutor{stream: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
		chunks := make(chan coreexecutor.StreamChunk, 1)
		chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"chunk":true}`)}
		close(chunks)
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}}
	h := newInterceptorHandler(t, model, executor, &sdkconfig.SDKConfig{})
	completions := make(chan pluginapi.RequestCompletion, 2)
	h.SetPluginHost(&handlerInterceptorTestHost{completeRequest: func(_ context.Context, completion pluginapi.RequestCompletion) { completions <- completion }})
	ctx, finish := coreusage.WithStreamDelivery(context.Background())
	defer finish(context.Canceled)
	data, _, errs := h.ExecuteStreamWithAuthManager(ctx, "openai", model, []byte(`{"stream":true}`), "")
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for data != nil || errs != nil {
		select {
		case _, ok := <-data:
			if !ok {
				data = nil
			}
		case errMsg, ok := <-errs:
			if ok && errMsg != nil {
				t.Fatalf("stream error: %+v", errMsg)
			}
			if !ok {
				errs = nil
			}
		case <-timer.C:
			t.Fatal("output channels blocked on delivery acknowledgement")
		}
	}
	select {
	case completion := <-completions:
		t.Fatalf("lifecycle completed before delivery: %+v", completion)
	default:
	}
	finish(nil)
	select {
	case completion := <-completions:
		if completion.Outcome != pluginapi.RequestCompletionSucceeded || completion.StatusCode != http.StatusOK || completion.Error != "" {
			t.Fatalf("completion = %+v", completion)
		}
	case <-timer.C:
		t.Fatal("missing lifecycle completion")
	}
	select {
	case duplicate := <-completions:
		t.Fatalf("duplicate completion: %+v", duplicate)
	default:
	}
}

func TestIssue6332LifecycleValidationFailureSurvivesDeliveryCancellation(t *testing.T) {
	model := "issue6332-lifecycle-validation"
	executor := &interceptorCaptureExecutor{stream: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
		chunks := make(chan coreexecutor.StreamChunk, 1)
		chunks <- coreexecutor.StreamChunk{Payload: []byte("data: invalid JSON\n\n")}
		close(chunks)
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}}
	h := newInterceptorHandler(t, model, executor, &sdkconfig.SDKConfig{})
	ctx, finish := coreusage.WithStreamDelivery(context.Background())
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer finish(context.Canceled)
	completions := make(chan pluginapi.RequestCompletion, 1)
	h.SetPluginHost(&handlerInterceptorTestHost{
		interceptStreamChunk: func(_ context.Context, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
			if len(req.Body) > 0 {
				// Make cancellation and the buffered error send ready together.
				finish(nil)
				cancel()
			}
			return pluginapi.StreamChunkInterceptResponse{Body: req.Body}
		},
		completeRequest: func(_ context.Context, completion pluginapi.RequestCompletion) { completions <- completion },
	})
	data, _, errs := h.ExecuteStreamWithAuthManager(ctx, "openai-response", model, []byte(`{"stream":true}`), "")
	for range data {
	}
	for range errs {
	}
	select {
	case completion := <-completions:
		if completion.Outcome != pluginapi.RequestCompletionFailed || completion.StatusCode != http.StatusBadGateway || !strings.Contains(completion.Error, "invalid SSE data JSON") {
			t.Fatalf("concrete failure replaced by delivery cleanup: %+v", completion)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("missing lifecycle completion")
	}
}
