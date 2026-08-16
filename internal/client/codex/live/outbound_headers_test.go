package live

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type liveHeaderFinalizerFunc func(context.Context, pluginapi.OutboundHeaderInterceptRequest) (http.Header, error)

func (f liveHeaderFinalizerFunc) FinalizeOutboundHeaders(ctx context.Context, req pluginapi.OutboundHeaderInterceptRequest) (http.Header, error) {
	return f(ctx, req)
}

func TestFinalizeCodexWebsocketHeaders(t *testing.T) {
	manager := auth.NewManager(nil, nil, nil)
	manager.SetOutboundHeaderFinalizer(liveHeaderFinalizerFunc(func(_ context.Context, req pluginapi.OutboundHeaderInterceptRequest) (http.Header, error) {
		if req.Provider != "codex" || req.Transport != pluginapi.OutboundTransportWebSocket {
			t.Fatalf("finalizer request identity = %#v", req)
		}
		headers := req.Headers.Clone()
		headers.Set("User-Agent", "live-plugin/1.0")
		return headers, nil
	}))
	handler := &Handler{authManager: manager}

	headers, errFinalize := handler.finalizeCodexWebsocketHeaders(context.Background(), http.Header{"User-Agent": {"host/1.0"}})
	if errFinalize != nil {
		t.Fatalf("finalizeCodexWebsocketHeaders() error = %v", errFinalize)
	}
	if headers.Get("User-Agent") != "live-plugin/1.0" {
		t.Fatalf("User-Agent = %q", headers.Get("User-Agent"))
	}
}
