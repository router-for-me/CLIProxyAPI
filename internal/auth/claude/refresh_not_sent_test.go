package claude

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
)

type claudeFailingDialer struct {
	calls *int32
	conn  net.Conn
	err   error
}

func (d claudeFailingDialer) Dial(_, _ string) (net.Conn, error) {
	atomic.AddInt32(d.calls, 1)
	if d.err != nil {
		return nil, d.err
	}
	return d.conn, nil
}

func newClaudeAuthWithDialer(dialer claudeFailingDialer) *ClaudeAuth {
	roundTripper := &utlsRoundTripper{dialer: dialer}
	roundTripper.transport = &http.Transport{
		ForceAttemptHTTP2: false,
		DialTLSContext:    roundTripper.dialTLSContext,
	}
	return &ClaudeAuth{httpClient: &http.Client{Transport: roundTripper}}
}

func TestRefreshTokensWithRetry_DefersWhenConnectionFailsBeforeSend(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) (net.Conn, error)
	}{
		{
			name: "dns",
			setup: func(*testing.T) (net.Conn, error) {
				return nil, &net.DNSError{Err: "no such host", Name: "platform.claude.com", IsNotFound: true}
			},
		},
		{
			name: "dial",
			setup: func(*testing.T) (net.Conn, error) {
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("network is unreachable")}
			},
		},
		{
			name: "tls-handshake",
			setup: func(t *testing.T) (net.Conn, error) {
				clientConn, serverConn := net.Pipe()
				if errClose := serverConn.Close(); errClose != nil {
					t.Fatalf("close server side: %v", errClose)
				}
				return clientConn, nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetClaudeRefreshState()
			defer resetClaudeRefreshState()

			var dials int32
			conn, errDial := tc.setup(t)
			auth := newClaudeAuthWithDialer(claudeFailingDialer{calls: &dials, conn: conn, err: errDial})

			_, err := auth.RefreshTokensWithRetry(context.Background(), "single-use-refresh-token", 3)
			if err == nil {
				t.Fatal("expected refresh error")
			}
			if !errors.Is(err, ErrClaudeRefreshDeferred) {
				t.Fatalf("error = %v, want a deferral so the unused refresh token is retried soon", err)
			}
			if !isRequestNotSent(err) {
				t.Fatalf("error = %v, want request-not-sent marker", err)
			}
			if got := atomic.LoadInt32(&dials); got != 1 {
				t.Fatalf("dial attempts = %d, want 1 (deferral returns to the caller instead of retrying in place)", got)
			}
		})
	}
}

func TestRefreshTokensWithRetry_DoesNotDeferAmbiguousTransportError(t *testing.T) {
	resetClaudeRefreshState()
	defer resetClaudeRefreshState()

	auth := &ClaudeAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("write: broken pipe")
			}),
		},
	}

	_, err := auth.RefreshTokensWithRetry(context.Background(), "single-use-refresh-token", 3)
	if err == nil {
		t.Fatal("expected refresh error")
	}
	if errors.Is(err, ErrClaudeRefreshDeferred) || isRequestNotSent(err) {
		t.Fatalf("error = %v, a failure after the request may have been sent must not be deferred", err)
	}
}
