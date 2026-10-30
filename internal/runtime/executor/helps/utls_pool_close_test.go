package helps

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var errChromePoolControlWrite = errors.New("injected HTTP/2 control-frame write failure")

type chromePoolControlWriteConn struct {
	net.Conn
	enabled     atomic.Bool
	started     chan struct{}
	released    chan struct{}
	closed      chan struct{}
	startOnce   sync.Once
	releaseOnce sync.Once
	closeOnce   sync.Once
	closeErr    error
}

func (c *chromePoolControlWriteConn) Write(p []byte) (int, error) {
	if !c.enabled.Load() {
		return c.Conn.Write(p)
	}
	c.startOnce.Do(func() { close(c.started) })
	<-c.released
	return 0, errChromePoolControlWrite
}

func (c *chromePoolControlWriteConn) releaseWrite() {
	c.releaseOnce.Do(func() { close(c.released) })
}

func (c *chromePoolControlWriteConn) Close() error {
	c.closeOnce.Do(func() {
		c.releaseWrite()
		c.closeErr = c.Conn.Close()
		close(c.closed)
	})
	return c.closeErr
}

func newChromePoolBlockedBodyClose(t *testing.T) (*pooledUtlsBody, *chromePoolControlWriteConn, context.CancelFunc) {
	t.Helper()
	released, release := chromePoolLifecycleGate()
	tlsConn, tracked, serverURL := newALPNTestConnection(t, "h2", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		// Leave unread response data so Body.Close must return flow-control
		// credit as well as reset the still-active stream.
		_, _ = io.WriteString(w, strings.Repeat("x", 16<<10))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-released:
		}
	}))
	gate := &chromePoolControlWriteConn{
		Conn: tracked.Conn, started: make(chan struct{}),
		released: make(chan struct{}), closed: make(chan struct{}),
	}
	// Install the wrapper before HTTP/2 starts its background reader.
	tracked.Conn = gate
	t.Cleanup(func() {
		_ = gate.Close()
		release()
	})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	entry, err := newUtlsClientConnection(ctx, tlsConn, strings.TrimPrefix(serverURL, "https://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(entry.closeConnection)
	resp := chromePoolRequest(t, ctx, entry.roundTrip, serverURL)
	body := &pooledUtlsBody{ReadCloser: resp.Body, pool: &utlsRoundTripper{}, conn: entry, ctx: ctx}
	t.Cleanup(func() {
		cancel()
		_ = gate.Close()
		_ = body.Close()
	})
	prefix := make([]byte, 1)
	if _, err := io.ReadFull(body, prefix); err != nil {
		t.Fatalf("read response prefix: %v", err)
	}
	gate.enabled.Store(true)
	return body, gate, cancel
}

func startChromePoolBlockedBodyClose(t *testing.T, body *pooledUtlsBody, gate *chromePoolControlWriteConn) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- body.Close() }()
	select {
	case <-gate.started:
	case err := <-done:
		t.Fatalf("Body.Close returned before the control-frame write blocked: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Body.Close did not write a control frame")
	}
	select {
	case err := <-done:
		t.Fatalf("Body.Close returned with the control-frame write still blocked: %v", err)
	default:
	}
	return done
}

func requireChromePoolBodyClosedAndDiscarded(t *testing.T, body *pooledUtlsBody, gate *chromePoolControlWriteConn, done <-chan error) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Body.Close did not finish after unblocking the control-frame write")
	}
	select {
	case <-gate.closed:
	default:
		t.Error("failed HTTP/2 connection was not closed")
	}
	if body.conn.usable() {
		t.Error("failed HTTP/2 connection is still usable")
	}
	body.pool.poolMu.Lock()
	idle := len(body.pool.idle)
	body.pool.poolMu.Unlock()
	if idle != 0 {
		t.Errorf("idle connections after failed Body.Close = %d, want 0", idle)
	}
}

func TestChromePoolHTTP2BodyCloseBlockedWriteCancellation(t *testing.T) {
	body, gate, cancel := newChromePoolBlockedBodyClose(t)
	done := startChromePoolBlockedBodyClose(t, body, gate)
	cancel()
	requireChromePoolBodyClosedAndDiscarded(t, body, gate, done)
}

func TestChromePoolHTTP2BodyCloseControlWriteFailure(t *testing.T) {
	body, gate, _ := newChromePoolBlockedBodyClose(t)
	done := startChromePoolBlockedBodyClose(t, body, gate)
	gate.releaseWrite()
	requireChromePoolBodyClosedAndDiscarded(t, body, gate, done)
}
