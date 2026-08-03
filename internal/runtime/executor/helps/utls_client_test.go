package helps

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type utlsClientRoundTripFunc func(*http.Request) (*http.Response, error)

func (f utlsClientRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type closeTrackingRoundTripper struct {
	response   *http.Response
	err        error
	closeCalls atomic.Int32
}

func (t *closeTrackingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return t.response, t.err
}

func (t *closeTrackingRoundTripper) CloseIdleConnections() {
	t.closeCalls.Add(1)
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

func TestNewUtlsHTTPClientDoesNotCloseContextRoundTripper(t *testing.T) {
	t.Parallel()

	shared := &closeTrackingRoundTripper{
		response: &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("{}")),
		},
	}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", shared)
	client := NewUtlsHTTPClient(ctx, nil, nil, 0)
	resp, err := client.Get("https://chatgpt.com/backend-api/codex/responses")
	if err != nil {
		t.Fatalf("client.Get returned error: %v", err)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatalf("response body close returned error: %v", errClose)
	}
	if got := shared.closeCalls.Load(); got != 0 {
		t.Fatalf("context RoundTripper close calls = %d, want 0", got)
	}
}

func TestCloseOwnedTransportRoundTripperClosesAfterResponseBody(t *testing.T) {
	t.Parallel()

	base := &closeTrackingRoundTripper{
		response: &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("{}")),
		},
	}
	transport := &closeOwnedTransportRoundTripper{base: base}
	req, err := http.NewRequest(http.MethodGet, "https://chatgpt.com", nil)
	if err != nil {
		t.Fatalf("http.NewRequest returned error: %v", err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip returned error: %v", err)
	}
	if got := base.closeCalls.Load(); got != 0 {
		t.Fatalf("close calls before body close = %d, want 0", got)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatalf("response body close returned error: %v", errClose)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatalf("second response body close returned error: %v", errClose)
	}
	if got := base.closeCalls.Load(); got != 1 {
		t.Fatalf("close calls after body close = %d, want 1", got)
	}
}

func TestCloseOwnedTransportRoundTripperClosesAfterRoundTripError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("dial failed")
	base := &closeTrackingRoundTripper{err: wantErr}
	transport := &closeOwnedTransportRoundTripper{base: base}
	req, err := http.NewRequest(http.MethodGet, "https://chatgpt.com", nil)
	if err != nil {
		t.Fatalf("http.NewRequest returned error: %v", err)
	}
	if _, err = transport.RoundTrip(req); !errors.Is(err, wantErr) {
		t.Fatalf("RoundTrip error = %v, want %v", err, wantErr)
	}
	if got := base.closeCalls.Load(); got != 1 {
		t.Fatalf("close calls after RoundTrip error = %d, want 1", got)
	}
}

func TestCloseOwnedTransportRoundTripperClosesEveryTrackedResponse(t *testing.T) {
	t.Parallel()

	const requestCount = 25
	base := &closeTrackingRoundTripper{}
	base.response = &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
	}
	transport := usageTTFTRoundTripper{
		base:     &closeOwnedTransportRoundTripper{base: base},
		reporter: &UsageReporter{},
	}

	for i := 0; i < requestCount; i++ {
		base.response.Body = io.NopCloser(strings.NewReader("{}"))
		req, err := http.NewRequest(http.MethodGet, "https://chatgpt.com", nil)
		if err != nil {
			t.Fatalf("request %d: http.NewRequest returned error: %v", i, err)
		}
		resp, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatalf("request %d: RoundTrip returned error: %v", i, err)
		}
		if _, err = io.ReadAll(resp.Body); err != nil {
			t.Fatalf("request %d: read response body: %v", i, err)
		}
		if err = resp.Body.Close(); err != nil {
			t.Fatalf("request %d: close response body: %v", i, err)
		}
	}

	if got := base.closeCalls.Load(); got != requestCount {
		t.Fatalf("close calls = %d, want %d", got, requestCount)
	}
}
