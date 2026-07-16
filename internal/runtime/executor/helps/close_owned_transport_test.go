package helps

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type closeOwnedTrackingRT struct {
	resp   *http.Response
	err    error
	closes atomic.Int32
}

func (t *closeOwnedTrackingRT) RoundTrip(*http.Request) (*http.Response, error) {
	return t.resp, t.err
}

func (t *closeOwnedTrackingRT) CloseIdleConnections() { t.closes.Add(1) }

// A per-request uTLS transport must be reclaimed exactly once, when the response
// body is closed — never earlier, and idempotently on a double Close.
func TestCloseOwnedTransport_ReclaimsOnceOnBodyClose(t *testing.T) {
	base := &closeOwnedTrackingRT{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("{}")),
	}}
	rt := &closeOwnedTransportRoundTripper{base: base}

	req, err := http.NewRequest(http.MethodGet, "https://chatgpt.com", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if got := base.closes.Load(); got != 0 {
		t.Fatalf("reclaimed before body close: %d", got)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("body close: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("second body close: %v", err)
	}
	if got := base.closes.Load(); got != 1 {
		t.Fatalf("CloseIdleConnections calls = %d, want 1 (once, idempotent across double Close)", got)
	}
}

// On a RoundTrip error (no body to tie to), the transport is reclaimed immediately.
func TestCloseOwnedTransport_ReclaimsOnRoundTripError(t *testing.T) {
	base := &closeOwnedTrackingRT{err: io.ErrUnexpectedEOF}
	rt := &closeOwnedTransportRoundTripper{base: base}

	req, err := http.NewRequest(http.MethodGet, "https://chatgpt.com", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if _, err := rt.RoundTrip(req); err == nil {
		t.Fatal("expected the RoundTrip error to surface")
	}
	if got := base.closes.Load(); got != 1 {
		t.Fatalf("CloseIdleConnections on error = %d, want 1", got)
	}
}
