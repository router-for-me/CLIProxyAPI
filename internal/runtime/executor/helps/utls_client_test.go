package helps

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type utlsClientRoundTripFunc func(*http.Request) (*http.Response, error)

func (f utlsClientRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNewUtlsHTTPClientUsesContextRoundTripperForProtectedHost(t *testing.T) {
	t.Parallel()

	called := false
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", utlsClientRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		if req.URL.Hostname() != "chatgpt.com" {
			t.Fatalf("hostname = %q, want chatgpt.com", req.URL.Hostname())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("{}")),
			Request:    req,
		}, nil
	}))

	client := NewUtlsHTTPClient(ctx, nil, nil, 0)
	resp, err := client.Get("https://chatgpt.com/backend-api/codex/responses")
	if err != nil {
		t.Fatalf("client.Get returned error: %v", err)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatalf("response body close returned error: %v", errClose)
	}
	if !called {
		t.Fatal("expected context RoundTripper to handle protected host request")
	}
}

// TestNewUtlsHTTPClientFallbackForcesHTTP11 verifies that requests to
// non-fingerprinted hosts (everything except the uTLS-protected Anthropic /
// ChatGPT domains) go through a fallback transport that negotiates HTTP/1.1
// only. Forcing HTTP/1.1 eliminates the HTTP/2 RST_STREAM(INTERNAL_ERROR)
// error class that flaky reseller upstreams emit mid-stream; a mid-response
// drop then surfaces as a clean EOF that the executor already terminates
// gracefully with a synthetic message_stop.
func TestNewUtlsHTTPClientFallbackForcesHTTP11(t *testing.T) {
	t.Parallel()

	client := NewUtlsHTTPClient(context.Background(), nil, nil, 0)

	fb, ok := client.Transport.(*fallbackRoundTripper)
	if !ok {
		t.Fatalf("client.Transport type = %T, want *fallbackRoundTripper", client.Transport)
	}

	tr, ok := fb.fallback.(*http.Transport)
	if !ok {
		t.Fatalf("fallback transport type = %T, want *http.Transport", fb.fallback)
	}

	if tr.ForceAttemptHTTP2 {
		t.Error("fallback ForceAttemptHTTP2 = true, want false (HTTP/1.1 only)")
	}
	if tr.TLSClientConfig == nil {
		t.Fatal("fallback TLSClientConfig = nil, want NextProtos pinned to http/1.1")
	}
	if got := tr.TLSClientConfig.NextProtos; len(got) != 1 || got[0] != "http/1.1" {
		t.Errorf("fallback TLSClientConfig.NextProtos = %v, want [http/1.1]", got)
	}
	if len(tr.TLSNextProto) != 0 {
		t.Errorf("fallback TLSNextProto has %d entries, want 0 (no implicit h2 upgrade)", len(tr.TLSNextProto))
	}

	// The shared global must not be mutated into HTTP/1.1; the client must own
	// an isolated clone.
	if def, ok := http.DefaultTransport.(*http.Transport); ok && !def.ForceAttemptHTTP2 {
		t.Error("http.DefaultTransport.ForceAttemptHTTP2 was flipped to false; expected an isolated clone, not mutation of the global")
	}
}

// TestForceHTTP11TransportNegotiatesHTTP11AgainstH2Server proves the real
// behavior: against a server that advertises HTTP/2 over ALPN, the forced
// transport still negotiates HTTP/1.1. The control assertion confirms the test
// server genuinely offers h2, so the HTTP/1.1 result is meaningful and not an
// artifact of the server only speaking HTTP/1.1.
func TestForceHTTP11TransportNegotiatesHTTP11AgainstH2Server(t *testing.T) {
	t.Parallel()

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	// srv.Client()'s transport trusts the test server's self-signed cert.
	base, ok := srv.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("test server client transport type = %T, want *http.Transport", srv.Client().Transport)
	}

	// Control: the unmodified client negotiates HTTP/2 with this server.
	controlResp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("control request returned error: %v", err)
	}
	controlProto := controlResp.ProtoMajor
	_ = controlResp.Body.Close()
	if controlProto != 2 {
		t.Fatalf("control negotiated HTTP/%d, want HTTP/2 (test server must offer h2 for this test to be meaningful)", controlProto)
	}

	// Forced: the same transport, pinned to HTTP/1.1, must negotiate HTTP/1.1.
	forced, ok := forceHTTP11Transport(base.Clone()).(*http.Transport)
	if !ok {
		t.Fatalf("forceHTTP11Transport returned %T, want *http.Transport", forceHTTP11Transport(base.Clone()))
	}
	client := &http.Client{Transport: forced}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("forced request returned error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.ProtoMajor != 1 {
		t.Fatalf("forced transport negotiated HTTP/%d, want HTTP/1.1", resp.ProtoMajor)
	}
}
