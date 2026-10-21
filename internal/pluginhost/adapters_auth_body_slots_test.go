package pluginhost

import (
	"context"
	"net/http"
	"testing"
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
