package pluginhost

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// With every decode slot taken, a decoding request waits and gives up with its context.
func TestAccessAdapterDecodeWaitsForSlot(t *testing.T) {
	saved := authDecodeSlots
	authDecodeSlots = make(chan struct{}, 1)
	authDecodeSlots <- struct{}{}
	defer func() { authDecodeSlots = saved }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, calls, _, authErr := authenticateWithBody(ctx, t, http.MethodPost, "/v1/responses", zstdCompress(t, []byte(`{"model":"gpt-5"}`)), "zstd", 0)
	if authErr == nil || calls != 0 || len(authDecodeSlots) != 1 {
		t.Fatalf("err %v, plugin calls %d, slots held %d", authErr, calls, len(authDecodeSlots))
	}
}

// When a client cancels while a guarded RPC auth call is still running, the decode slot stays
// held until the underlying inner plugin call finishes.
func TestAccessAdapterCanceledRPCKeepsSlotUntilInnerCallFinishes(t *testing.T) {
	saved := authDecodeSlots
	authDecodeSlots = make(chan struct{}, 1)
	defer func() { authDecodeSlots = saved }()

	inner := &blockingGuardPluginClient{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	guarded := newGuardedPluginClient(inner)
	host := New()
	host.runtimeConfig = &config.Config{}
	adapter := newAccessAdapterForTest(host, "auth-plugin", rpcFrontendAuthProvider{
		rpcPluginAdapter: &rpcPluginAdapter{
			id:     "auth-plugin",
			host:   host,
			client: guarded,
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	compressed := zstdCompress(t, []byte(`{"model":"gpt-5"}`))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(compressed))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "zstd")

	authDone := make(chan struct{})
	go func() {
		_, _ = adapter.Authenticate(ctx, req)
		close(authDone)
	}()

	<-inner.started
	if len(authDecodeSlots) != 1 {
		t.Fatalf("slots held while inner call active = %d, want 1", len(authDecodeSlots))
	}

	cancel()
	<-authDone
	if len(authDecodeSlots) != 1 {
		t.Fatalf("slots held after canceled Authenticate returned while inner call still blocked = %d, want 1", len(authDecodeSlots))
	}

	close(inner.release)
	guarded.Shutdown()
	if len(authDecodeSlots) != 0 {
		t.Fatalf("slots held after inner call finished = %d, want 0", len(authDecodeSlots))
	}
}
