package helps

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func recoveryTestServer(t *testing.T, handler http.Handler) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	return server, roots
}

func TestUtlsRetriesFailedHandshakeBeforeSendingBody(t *testing.T) {
	for _, dialTimeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "connection reset", true: "dial timeout"}[dialTimeout], func(t *testing.T) {
			var requests atomic.Int32
			server, roots := recoveryTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil || string(body) != "send this exactly once" {
					t.Errorf("request body: %q, %v", body, errRead)
				}
				_, _ = w.Write([]byte("ok"))
			}))
			var dials atomic.Int32
			transport := &utlsRoundTripper{rootCAs: roots}
			transport.dialer = contextDialerFunc(func(ctx context.Context, network, addr string) (net.Conn, error) {
				if dials.Add(1) == 1 {
					if dialTimeout {
						// What the OS reports when a SYN goes unanswered.
						return nil, &net.OpError{Op: "dial", Net: "tcp", Err: timeoutError{}}
					}
					client, remote := net.Pipe()
					go func() {
						defer func() { _ = remote.Close() }()
						_, _ = remote.Read(make([]byte, 4096))
					}()
					return client, nil
				}
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			})
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL, bytes.NewBufferString("send this exactly once"))
			resp, errRoundTrip := transport.RoundTrip(req)
			if errRoundTrip != nil {
				t.Fatal(errRoundTrip)
			}
			payload, errRead := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if errRead != nil || string(payload) != "ok" {
				t.Fatalf("response: %q, %v", payload, errRead)
			}
			if requests.Load() != 1 || dials.Load() != 2 {
				t.Fatalf("requests=%d dials=%d", requests.Load(), dials.Load())
			}
		})
	}
}

func TestUtlsDoesNotRetryCertificateFailure(t *testing.T) {
	server, _ := recoveryTestServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("untrusted server received request") }))
	var dials atomic.Int32
	transport := &utlsRoundTripper{rootCAs: x509.NewCertPool(), dialer: contextDialerFunc(func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	})}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
	_, errRoundTrip := transport.RoundTrip(req)
	var certificateErr x509.UnknownAuthorityError
	if !errors.As(errRoundTrip, &certificateErr) || dials.Load() != 1 {
		t.Fatalf("error=%v dials=%d", errRoundTrip, dials.Load())
	}
}

func TestUtlsNeverReplaysAfterRequestWasSent(t *testing.T) {
	var requests, dials atomic.Int32
	server, roots := recoveryTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		requests.Add(1)
		panic(http.ErrAbortHandler)
	}))
	transport := &utlsRoundTripper{rootCAs: roots, dialer: contextDialerFunc(func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	})}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL, bytes.NewBufferString("do not replay"))
	_, errRoundTrip := transport.RoundTrip(req)
	if errRoundTrip == nil || requests.Load() != 1 || dials.Load() != 1 {
		t.Fatalf("error=%v requests=%d dials=%d", errRoundTrip, requests.Load(), dials.Load())
	}
}

func TestUtlsConnectionRetriesAreBoundedAndClassified(t *testing.T) {
	var dials atomic.Int32
	transport := &utlsRoundTripper{dialer: contextDialerFunc(func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: io.EOF}
	})}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://chatgpt.com", nil)
	_, errRoundTrip := transport.RoundTrip(req)
	var status interface{ StatusCode() int }
	if !errors.As(errRoundTrip, &status) || status.StatusCode() != 502 || dials.Load() != utlsConnectionAttempts {
		t.Fatalf("error=%v attempts=%d", errRoundTrip, dials.Load())
	}
	var retryAfter interface{ RetryAfter() *time.Duration }
	if !errors.As(errRoundTrip, &retryAfter) || *retryAfter.RetryAfter() != 5*time.Second {
		t.Fatalf("missing short transport retry delay: %v", errRoundTrip)
	}
}

func TestUtlsConnectionSetupUsesRequestContextWithoutOwnDeadline(t *testing.T) {
	server, roots := recoveryTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	transport := &utlsRoundTripper{rootCAs: roots, dialer: contextDialerFunc(func(ctx context.Context, network, addr string) (net.Conn, error) {
		if _, hasDeadline := ctx.Deadline(); hasDeadline {
			t.Error("connection setup must not add a deadline to the request context")
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	})}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
	resp, errRoundTrip := transport.RoundTrip(req)
	if errRoundTrip != nil {
		t.Fatal(errRoundTrip)
	}
	body, errRead := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if errRead != nil || string(body) != "ok" {
		t.Fatalf("body=%q error=%v", body, errRead)
	}
}

func TestUtlsCallerCancellationStopsRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var dials atomic.Int32
	transport := &utlsRoundTripper{dialer: contextDialerFunc(func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		cancel()
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: io.EOF}
	})}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com", nil)
	_, errRoundTrip := transport.RoundTrip(req)
	if !errors.Is(errRoundTrip, context.Canceled) || dials.Load() != 1 {
		t.Fatalf("error=%v dials=%d", errRoundTrip, dials.Load())
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
