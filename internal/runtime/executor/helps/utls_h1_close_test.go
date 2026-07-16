package helps

import (
	"net"
	"sync/atomic"
	"testing"
)

// fakeH1Conn is a net.Conn whose only real behavior is counting Close calls; the
// embedded nil net.Conn satisfies the rest of the interface (unused in this test).
type fakeH1Conn struct {
	net.Conn
	closed atomic.Int32
}

func (c *fakeH1Conn) Close() error { c.closed.Add(1); return nil }

// CloseIdleConnections must close the ACTUAL pooled sockets (not merely be invoked)
// and drain the idle map, since a per-request h1 client leaks one socket per request
// otherwise. Idempotent: a second call after the map is drained closes nothing again.
func TestUtlsH1RoundTripper_CloseIdleConnections_ClosesRealSockets(t *testing.T) {
	c1 := &fakeH1Conn{}
	c2 := &fakeH1Conn{}
	rt := &utlsH1RoundTripper{idle: map[string][]*utlsH1Conn{
		"chatgpt.com":       {{conn: c1}},
		"api.anthropic.com": {{conn: c2}},
	}}

	rt.CloseIdleConnections()

	if got := c1.closed.Load(); got != 1 {
		t.Fatalf("chatgpt.com socket Close count = %d, want 1", got)
	}
	if got := c2.closed.Load(); got != 1 {
		t.Fatalf("api.anthropic.com socket Close count = %d, want 1", got)
	}

	rt.mu.Lock()
	remaining := len(rt.idle)
	rt.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("idle map not drained: %d hosts remain", remaining)
	}

	// Second call is a no-op — the sockets must not be closed a second time.
	rt.CloseIdleConnections()
	if got := c1.closed.Load(); got != 1 {
		t.Fatalf("socket closed again on second CloseIdleConnections: count = %d, want 1", got)
	}
}
