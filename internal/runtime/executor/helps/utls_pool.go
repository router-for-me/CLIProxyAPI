package helps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	tls "github.com/refraction-networking/utls"
	internalcache "github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
)

const chromeTransportCacheCapacity = 64
const chromeMaxIdleConnections = 8

// ClientConn can fail before taking ownership of a request body. Close it on
// transport errors too, without racing or double-closing native async cleanup.
type utlsRequestBody struct {
	io.ReadCloser
	once sync.Once
	err  error
}

func (b *utlsRequestBody) Close() error {
	b.once.Do(func() { b.err = b.ReadCloser.Close() })
	return b.err
}

func wrapUtlsRequestBody(req *http.Request) *http.Request {
	if req.Body == nil || req.Body == http.NoBody {
		return req
	}
	if _, ok := req.Body.(*utlsRequestBody); ok {
		return req
	}
	wrapped := req.WithContext(req.Context())
	wrapped.Body = &utlsRequestBody{ReadCloser: req.Body}
	return wrapped
}

type chromeTransportKey struct {
	credentialID string
	proxyURL     string
	anonymous    *cliproxyauth.Auth
}

var chromeTransportCache = internalcache.NewBoundedLRU[chromeTransportKey, *utlsRoundTripper](
	chromeTransportCacheCapacity,
	func(_ chromeTransportKey, transport *utlsRoundTripper) { transport.retire() },
)

func cachedChromeRoundTripper(proxyURL string, auth *cliproxyauth.Auth) *utlsRoundTripper {
	key := chromeTransportKey{proxyURL: proxyURL}
	if auth != nil {
		key.credentialID = strings.TrimSpace(auth.ID)
		if key.credentialID == "" {
			// Unregistered SDK credentials have no stable identity. Do not share
			// their sockets with another credential or retain token values as keys.
			key.anonymous = auth
		}
	}
	return chromeTransportCache.GetOrAdd(key, func() *utlsRoundTripper {
		return newUtlsRoundTripper(proxyURL)
	})
}

// An exclusive lease preserves the existing one-request-per-connection behavior.
// HTTP/2 is reused sequentially; concurrent streams use separate connections.
type utlsClientConnection struct {
	address             string
	roundTrip           http.RoundTripper
	close               func() error
	usable              func() bool
	raw                 net.Conn
	reuseAfterBodyClose bool
}

// HTTP/2 can discover a failed control-frame write during Body.Close. Latch I/O
// failures as well as checking protocol state before returning a socket to idle.
type utlsPoolConn struct {
	net.Conn
	failed atomic.Bool
}

func (c *utlsPoolConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if err != nil {
		c.failed.Store(true)
	}
	return n, err
}

func (c *utlsPoolConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if err != nil || n != len(p) {
		c.failed.Store(true)
	}
	return n, err
}

func closeUtlsOnCancellation(ctx context.Context, raw net.Conn) func() {
	cancelled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = raw.Close()
		close(cancelled)
	})
	return func() {
		if !stop() {
			<-cancelled
		}
	}
}

func (c *utlsClientConnection) closeConnection() {
	if err := c.close(); err != nil && !errors.Is(err, net.ErrClosed) {
		log.Debugf("utls: close upstream connection: %v", err)
	}
}

func newUtlsClientConnection(ctx context.Context, conn *tls.UConn, address string) (out *utlsClientConnection, err error) {
	// HTTP/2 initialization writes the preface synchronously. Cancellation must
	// close the raw socket even before a ClientConn owns it. Join this callback
	// before returning, so an old request cannot close a later request's socket.
	stop := closeUtlsOnCancellation(ctx, conn.NetConn())
	defer func() {
		stop()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if err != nil {
			if out != nil {
				out.closeConnection()
				out = nil
			} else {
				_ = conn.NetConn().Close()
			}
		}
	}()
	tracked := &utlsPoolConn{Conn: conn}
	closeClient := func(close func() error) func() error {
		return func() error {
			// Close the socket first so TLS close-notify or HTTP/2 control-frame
			// writes cannot delay disposal of an unusable connection.
			_ = conn.NetConn().Close()
			return close()
		}
	}

	switch protocol := conn.ConnectionState().NegotiatedProtocol; protocol {
	case "h2":
		client, errCreate := (&http2.Transport{}).NewClientConn(tracked)
		if errCreate != nil {
			return nil, fmt.Errorf("utls: initialize HTTP/2 connection: %w", errCreate)
		}
		return &utlsClientConnection{
			address: address, roundTrip: client, close: closeClient(client.Close), raw: conn.NetConn(),
			reuseAfterBodyClose: true,
			usable: func() bool {
				state := client.State()
				return !tracked.failed.Load() && !state.Closed && !state.Closing &&
					state.StreamsActive == 0 && state.StreamsReserved == 0 && state.StreamsPending == 0 &&
					client.CanTakeNewRequest()
			},
		}, nil
	case "", "http/1.1":
		transport := &http.Transport{
			DialTLSContext: func(context.Context, string, string) (net.Conn, error) { return tracked, nil },
		}
		// A single ClientConn never transparently resends a POST after failure.
		// uTLS is not crypto/tls.Conn, so net/http treats it as HTTP/1 here;
		// ALPN was already checked above before entering this branch.
		client, errCreate := transport.NewClientConn(ctx, "https", address)
		if errCreate != nil {
			return nil, fmt.Errorf("utls: initialize HTTP/1 connection: %w", errCreate)
		}
		return &utlsClientConnection{
			address: address, roundTrip: client, close: closeClient(client.Close), raw: conn.NetConn(),
			usable: func() bool { return !tracked.failed.Load() && client.Err() == nil && client.Available() > 0 },
		}, nil
	default:
		return nil, fmt.Errorf("utls: unsupported negotiated protocol %q", protocol)
	}
}

func (t *utlsRoundTripper) takeIdle(address string) *utlsClientConnection {
	for {
		t.poolMu.Lock()
		var found *utlsClientConnection
		for i := len(t.idle) - 1; i >= 0; i-- {
			if t.idle[i].address == address {
				found = t.idle[i]
				t.idle = append(t.idle[:i], t.idle[i+1:]...)
				break
			}
		}
		t.poolMu.Unlock()
		if found == nil || found.usable() {
			return found
		}
		found.closeConnection()
	}
}

func (t *utlsRoundTripper) putIdle(conn *utlsClientConnection) {
	if !conn.usable() {
		conn.closeConnection()
		return
	}
	t.poolMu.Lock()
	var discard *utlsClientConnection
	if t.retired {
		discard = conn
	} else {
		if len(t.idle) >= chromeMaxIdleConnections {
			discard = t.idle[0]
			t.idle = t.idle[1:]
		}
		t.idle = append(t.idle, conn)
	}
	t.poolMu.Unlock()
	if discard != nil {
		discard.closeConnection()
	}
}

// CloseIdleConnections does not interrupt active responses or disable reuse.
func (t *utlsRoundTripper) CloseIdleConnections() {
	t.drainIdle(false)
}

// A client can outlive its cache entry. Retired transports remain usable, but
// never retain returned sockets, including leases active during eviction.
func (t *utlsRoundTripper) retire() { t.drainIdle(true) }

func (t *utlsRoundTripper) drainIdle(retire bool) {
	t.poolMu.Lock()
	t.retired = t.retired || retire
	idle := t.idle
	t.idle = nil
	t.poolMu.Unlock()
	for _, conn := range idle {
		conn.closeConnection()
	}
}

type pooledUtlsBody struct {
	io.ReadCloser
	pool       *utlsRoundTripper
	conn       *utlsClientConnection
	ctx        context.Context
	eof        atomic.Bool
	readFailed atomic.Bool
	once       sync.Once
	err        error
}

func (b *pooledUtlsBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		b.eof.Store(true)
	} else if err != nil {
		b.readFailed.Store(true)
	}
	return n, err
}

func (b *pooledUtlsBody) Close() error {
	b.once.Do(func() {
		// Closing an HTTP/2 body may flush RST_STREAM/WINDOW_UPDATE. Preserve
		// cancellation even if the peer has stopped reading those writes.
		stop := closeUtlsOnCancellation(b.ctx, b.conn.raw)
		b.err = b.ReadCloser.Close()
		stop()
		// Codex stops at its terminal SSE event, often before HTTP EOF. HTTP/2
		// safely discards unread bytes per stream; HTTP/1 still needs full EOF.
		if b.err == nil && !b.readFailed.Load() && b.ctx.Err() == nil && (b.eof.Load() || b.conn.reuseAfterBodyClose) {
			b.pool.putIdle(b.conn)
		} else {
			b.conn.closeConnection()
		}
	})
	return b.err
}
