package executor

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/wsrelay"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/outbound"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type executorHeaderFinalizerFunc func(context.Context, pluginapi.OutboundHeaderInterceptRequest) (http.Header, error)

func (f executorHeaderFinalizerFunc) FinalizeOutboundHeaders(ctx context.Context, req pluginapi.OutboundHeaderInterceptRequest) (http.Header, error) {
	return f(ctx, req)
}

func TestFinalizeAIStudioRequestAppliesOutboundHeaders(t *testing.T) {
	ctx := outbound.WithHeaderFinalizer(context.Background(), executorHeaderFinalizerFunc(func(_ context.Context, req pluginapi.OutboundHeaderInterceptRequest) (http.Header, error) {
		if req.Provider != "aistudio" || req.Transport != pluginapi.OutboundTransportHTTP {
			t.Fatalf("finalizer request identity = %#v", req)
		}
		headers := req.Headers.Clone()
		headers.Set("User-Agent", "aistudio-plugin/1.0")
		return headers, nil
	}))
	req := &wsrelay.HTTPRequest{Headers: http.Header{"User-Agent": {"host/1.0"}}}

	if errFinalize := finalizeAIStudioRequest(ctx, req); errFinalize != nil {
		t.Fatalf("finalizeAIStudioRequest() error = %v", errFinalize)
	}
	if req.Headers.Get("User-Agent") != "aistudio-plugin/1.0" {
		t.Fatalf("User-Agent = %q", req.Headers.Get("User-Agent"))
	}
}

func TestWebsocketDialFailsClosedWhenOutboundHeaderFinalizerRejects(t *testing.T) {
	errRejected := errors.New("header plugin rejected websocket")
	tests := []struct {
		name     string
		provider string
		dial     func(context.Context) error
	}{
		{
			name:     "codex",
			provider: "codex",
			dial: func(ctx context.Context) error {
				_, _, _, errDial := NewCodexWebsocketsExecutor(&config.Config{}).dialCodexWebsocket(ctx, nil, "ws://127.0.0.1:1", http.Header{"User-Agent": {"host/1.0"}})
				return errDial
			},
		},
		{
			name:     "xai",
			provider: "xai",
			dial: func(ctx context.Context) error {
				_, _, _, errDial := NewXAIWebsocketsExecutor(&config.Config{}).dialXAIWebsocket(ctx, nil, "ws://127.0.0.1:1", http.Header{"User-Agent": {"host/1.0"}})
				return errDial
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls int
			ctx := outbound.WithHeaderFinalizer(context.Background(), executorHeaderFinalizerFunc(func(_ context.Context, req pluginapi.OutboundHeaderInterceptRequest) (http.Header, error) {
				calls++
				if req.Provider != test.provider || req.Transport != pluginapi.OutboundTransportWebSocket {
					t.Fatalf("finalizer request identity = %#v", req)
				}
				return nil, errRejected
			}))
			if errDial := test.dial(ctx); !errors.Is(errDial, errRejected) {
				t.Fatalf("dial error = %v, want %v", errDial, errRejected)
			}
			if calls != 1 {
				t.Fatalf("finalizer calls = %d, want 1", calls)
			}
		})
	}
}
