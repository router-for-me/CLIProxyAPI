package helps

import (
	"context"
	stdtls "crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tls "github.com/refraction-networking/utls"
)

type alpnTestConn struct {
	net.Conn
	closed chan struct{}
	once   sync.Once
}

func (c *alpnTestConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.closed) })
	return err
}

func (c *alpnTestConn) requireClosed(t *testing.T) {
	t.Helper()
	select {
	case <-c.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("TLS connection was not closed")
	}
}

func newALPNTestConnection(t *testing.T, protocol string, handler http.Handler) (*tls.UConn, *alpnTestConn, string) {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = protocol == "h2"
	server.TLS = &stdtls.Config{NextProtos: []string{}}
	if protocol != "" {
		server.TLS.NextProtos = []string{protocol}
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	rootCAs := x509.NewCertPool()
	rootCAs.AddCert(server.Certificate())
	serverURL, errParse := url.Parse(server.URL)
	if errParse != nil {
		t.Fatal(errParse)
	}
	conn, errDial := (&net.Dialer{}).DialContext(t.Context(), "tcp", serverURL.Host)
	if errDial != nil {
		t.Fatal(errDial)
	}
	tracked := &alpnTestConn{Conn: conn, closed: make(chan struct{})}
	tlsConn := tls.UClient(tracked, &tls.Config{ServerName: serverURL.Hostname(), RootCAs: rootCAs}, tls.HelloChrome_Auto)
	t.Cleanup(func() {
		if errClose := tlsConn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close test TLS connection: %v", errClose)
		}
	})
	if errHandshake := tlsConn.HandshakeContext(t.Context()); errHandshake != nil {
		t.Fatal(errHandshake)
	}
	if got := tlsConn.ConnectionState().NegotiatedProtocol; got != protocol {
		t.Fatalf("negotiated protocol = %q, want %q", got, protocol)
	}
	return tlsConn, tracked, server.URL
}

func TestUtlsRoundTripUsesNegotiatedProtocol(t *testing.T) {
	for _, protocol := range []string{"h2", "http/1.1", ""} {
		for _, chunked := range []bool{false, true} {
			name := protocol
			if name == "" {
				name = "no-alpn"
			}
			if chunked {
				name += "/unknown-content-length"
			}
			t.Run(name, func(t *testing.T) {
				var requests atomic.Int32
				tlsConn, tracked, serverURL := newALPNTestConnection(t, protocol, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					wantMajor := 1
					if protocol == "h2" {
						wantMajor = 2
					}
					if r.ProtoMajor != wantMajor {
						t.Errorf("HTTP version = %s, want major %d", r.Proto, wantMajor)
					}
					if r.Method != http.MethodPost || r.URL.RequestURI() != "/calls?intent=test" || r.Header.Get("X-Test") != "preserved" {
						t.Errorf("request metadata not preserved: %s %s, X-Test=%q", r.Method, r.URL.RequestURI(), r.Header.Get("X-Test"))
					}
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil || string(body) != "offer" {
						t.Errorf("request body = %q, error = %v", body, errRead)
					}
					w.Header().Set("X-Reply", "preserved")
					w.WriteHeader(http.StatusCreated)
					if _, errWrite := io.WriteString(w, "answer"); errWrite != nil {
						t.Errorf("write response: %v", errWrite)
					}
				}))
				req, errRequest := http.NewRequestWithContext(t.Context(), http.MethodPost, serverURL+"/calls?intent=test", strings.NewReader("offer"))
				if errRequest != nil {
					t.Fatal(errRequest)
				}
				req.Header.Set("X-Test", "preserved")
				if chunked {
					req.ContentLength = -1
				}
				resp, errRoundTrip := roundTripUtlsConnection(req, tlsConn)
				if errRoundTrip != nil {
					t.Fatal(errRoundTrip)
				}
				body, errRead := io.ReadAll(resp.Body)
				if errRead != nil {
					t.Fatal(errRead)
				}
				if errClose := resp.Body.Close(); errClose != nil {
					t.Fatal(errClose)
				}
				if resp.StatusCode != http.StatusCreated || resp.Header.Get("X-Reply") != "preserved" || string(body) != "answer" {
					t.Fatalf("response not preserved: status=%d header=%q body=%q", resp.StatusCode, resp.Header.Get("X-Reply"), body)
				}
				if requests.Load() != 1 {
					t.Fatalf("request count = %d, want exactly one", requests.Load())
				}
				tracked.requireClosed(t)
			})
		}
	}
}

func TestUtlsRoundTripCancellationAfterHandshake(t *testing.T) {
	for _, protocol := range []string{"h2", "http/1.1", ""} {
		t.Run(protocol, func(t *testing.T) {
			requestReceived := make(chan struct{})
			releaseHandler := make(chan struct{})
			tlsConn, tracked, serverURL := newALPNTestConnection(t, protocol, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(requestReceived)
				select {
				case <-r.Context().Done():
				case <-releaseHandler:
				}
			}))
			t.Cleanup(func() { close(releaseHandler) })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, serverURL, strings.NewReader("offer"))
			if errRequest != nil {
				t.Fatal(errRequest)
			}
			done := make(chan error, 1)
			go func() {
				resp, errRoundTrip := roundTripUtlsConnection(req, tlsConn)
				if resp != nil {
					errRoundTrip = errors.Join(errRoundTrip, resp.Body.Close())
				}
				done <- errRoundTrip
			}()
			select {
			case <-requestReceived:
			case <-time.After(5 * time.Second):
				t.Fatal("server did not receive request")
			}
			cancel()
			select {
			case errRoundTrip := <-done:
				if !errors.Is(errRoundTrip, context.Canceled) {
					t.Fatalf("RoundTrip error = %v, want context canceled", errRoundTrip)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("RoundTrip did not stop after context cancellation")
			}
			tracked.requireClosed(t)
		})
	}
}

func TestUtlsHTTP1FailureDoesNotReplayPost(t *testing.T) {
	var requests atomic.Int32
	tlsConn, tracked, serverURL := newALPNTestConnection(t, "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		conn, _, errHijack := w.(http.Hijacker).Hijack()
		if errHijack != nil {
			t.Errorf("hijack connection: %v", errHijack)
			return
		}
		if errClose := conn.Close(); errClose != nil {
			t.Errorf("close server connection: %v", errClose)
		}
	}))
	req, errRequest := http.NewRequestWithContext(t.Context(), http.MethodPost, serverURL, strings.NewReader("offer"))
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	resp, errRoundTrip := roundTripUtlsConnection(req, tlsConn)
	if resp != nil {
		if errClose := resp.Body.Close(); errClose != nil {
			t.Errorf("close unexpected response: %v", errClose)
		}
	}
	if errRoundTrip == nil {
		t.Fatal("expected error from upstream connection closure")
	}
	if requests.Load() != 1 {
		t.Fatalf("request count = %d, want exactly one", requests.Load())
	}
	tracked.requireClosed(t)
}
