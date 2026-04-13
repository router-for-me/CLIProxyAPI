package helps

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func TestPrewarmableTransportUsesPrewarmedConnFirst(t *testing.T) {
	t.Parallel()

	serverConn, clientConn := net.Pipe()
	defer func() {
		_ = serverConn.Close()
	}()

	var dialCount atomic.Int32
	base := &http.Transport{
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			dialCount.Add(1)
			return nil, nil
		},
	}

	wrapped := newPrewarmableTransport(base)
	targetURL, errParse := url.Parse("http://example.com")
	if errParse != nil {
		t.Fatalf("Parse() error = %v", errParse)
	}

	wrapped.storePrewarmedConn("tcp", canonicalAddr(targetURL.Hostname(), targetURL.Port(), targetURL.Scheme), clientConn, time.Now().Add(prewarmedConnTTL))

	gotConn, errDial := wrapped.dialContext(context.Background(), "tcp", "example.com:80")
	if errDial != nil {
		t.Fatalf("dialContext() error = %v", errDial)
	}
	if gotConn != clientConn {
		t.Fatalf("dialContext() did not return prewarmed conn")
	}
	if got := dialCount.Load(); got != 0 {
		t.Fatalf("dial count = %d, want 0", got)
	}
}

func TestWrapPrewarmableRoundTripperWrapsHTTPTransport(t *testing.T) {
	t.Parallel()

	base := &http.Transport{}
	wrapped := wrapPrewarmableRoundTripper(base)
	if _, ok := wrapped.(*prewarmableTransport); !ok {
		t.Fatalf("wrapped transport type = %T", wrapped)
	}
}
