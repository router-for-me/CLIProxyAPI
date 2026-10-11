package claudemaster

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The signal that stops the server must not end the requests the drain is waiting for: a stream
// running through the real backend handler keeps going when Serve's context is cancelled, and
// ends only when its own request ends.
func TestServeContextCancellationDoesNotCancelRunningRequests(t *testing.T) {
	serveCtx, stopServer := context.WithCancel(t.Context())
	defer stopServer()
	capture := &backendCapture{streamWait: true, started: make(chan struct{})}
	handler, _ := backendTestHandler(t, serveBackendLifetime(serveCtx), capture)

	requestCtx, endRequest := context.WithCancel(t.Context())
	defer endRequest()
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"master","stream":true,"messages":[{"role":"user","content":"hello"}]}`)).WithContext(requestCtx)
	served := make(chan struct{})
	go func() { handler.ServeHTTP(httptest.NewRecorder(), r); close(served) }()

	select {
	case <-capture.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream never started")
	}
	stopServer()
	select {
	case <-served:
		t.Fatal("cancelling the serve context ended a running request; the drain cannot wait for it")
	case <-time.After(200 * time.Millisecond):
	}
	endRequest()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("the request did not end with its own context")
	}
}
