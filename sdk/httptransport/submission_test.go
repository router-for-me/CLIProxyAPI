package httptransport

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSubmissionGuardPreventsInternalPOSTReplay(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		conn, _, errHijack := w.(http.Hijacker).Hijack()
		if errHijack != nil {
			t.Error(errHijack)
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()
	client := Decorate(WithSubmissionTracking(context.Background()), server.Client())
	warm, errWarm := client.Get(server.URL)
	if errWarm != nil {
		t.Fatal(errWarm)
	}
	_, _ = io.Copy(io.Discard, warm.Body)
	_ = warm.Body.Close()
	var reused, wrote atomic.Bool
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused.Store(info.Reused) }, WroteRequest: func(info httptrace.WroteRequestInfo) { wrote.Store(info.Err == nil) }}
	request, errRequest := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace), http.MethodPost, server.URL, strings.NewReader("non-idempotent operation"))
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	request.Header.Set("Idempotency-Key", "synthetic-key")
	_, errDo := client.Do(request)
	if !IsOutcomeUnknown(errDo) {
		t.Fatalf("error=%v, want unknown outcome", errDo)
	}
	if !reused.Load() || !wrote.Load() {
		t.Fatalf("existing trace lost or warm connection not reused: reused=%v wrote=%v", reused.Load(), wrote.Load())
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("requests=%d, want warm GET plus one POST", got)
	}
	// An executor's internal fallback must not bypass the manager's terminal decision.
	_, errRetry := client.Post(server.URL, "text/plain", strings.NewReader("same operation"))
	if !IsOutcomeUnknown(errRetry) || calls.Load() != 2 {
		t.Fatalf("internal retry: error=%v requests=%d", errRetry, calls.Load())
	}
}

func TestSubmissionGuardHTTPRefusalIsNotAcceptance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	ctx := WithSubmissionTracking(context.Background())
	client := Decorate(ctx, server.Client())
	response, errDo := client.Post(server.URL, "text/plain", strings.NewReader("request"))
	if errDo != nil {
		t.Fatal(errDo)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatal(response.StatusCode)
	}
	if err := ClassifySubmissionError(ctx, io.EOF); IsOutcomeUnknown(err) {
		t.Fatal("explicit refusal marked accepted")
	}
}
