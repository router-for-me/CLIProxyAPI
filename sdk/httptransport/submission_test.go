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
	for _, method := range []string{http.MethodPost, http.MethodPatch} {
		for _, tc := range []struct {
			name   string
			body   func() io.Reader
			header string
		}{
			{"payload", func() io.Reader { return strings.NewReader("non-idempotent operation") }, "Idempotency-Key"},
			{"nil", func() io.Reader { return nil }, "Idempotency-Key"},
			{"no-body", func() io.Reader { return http.NoBody }, "Idempotency-Key"},
			{"nil-x-key", func() io.Reader { return nil }, "X-Idempotency-Key"},
			{"no-body-x-key", func() io.Reader { return http.NoBody }, "X-Idempotency-Key"},
		} {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					bodyBytes, _ := io.Copy(io.Discard, r.Body)
					if calls.Add(1) == 1 {
						w.WriteHeader(http.StatusNoContent)
						return
					}
					if tc.name != "payload" && (r.ContentLength != 0 || len(r.TransferEncoding) != 0) {
						t.Errorf("zero framing changed: length=%d transfer=%v", r.ContentLength, r.TransferEncoding)
					}
					if tc.name != "payload" && bodyBytes != 0 {
						t.Errorf("empty request acquired a payload: bytes=%d", bodyBytes)
					}
					if r.Header.Get(tc.header) != "synthetic-key" {
						t.Errorf("idempotency header lost: %q", r.Header.Get(tc.header))
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
				request, errRequest := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace), method, server.URL, tc.body())
				if errRequest != nil {
					t.Fatal(errRequest)
				}
				request.Header.Set(tc.header, "synthetic-key")
				originalBody := request.Body
				_, errDo := client.Do(request)
				if request.Header.Get(tc.header) != "synthetic-key" {
					t.Fatal("original request header changed")
				}
				if request.Body != originalBody {
					t.Fatal("original request body changed")
				}
				if !IsOutcomeUnknown(errDo) {
					t.Fatalf("error=%v, want unknown outcome", errDo)
				}
				t.Logf("upstream requests including warm GET=%d, reused=%t", calls.Load(), reused.Load())
				if got := calls.Load(); got != 2 {
					t.Fatalf("requests=%d, want warm GET plus one POST", got)
				}
				if !reused.Load() || !wrote.Load() {
					t.Fatalf("existing trace lost or warm connection not reused: reused=%v wrote=%v", reused.Load(), wrote.Load())
				}
				// An executor's internal fallback must not bypass the manager's terminal decision.
				_, errRetry := client.Post(server.URL, "text/plain", strings.NewReader("same operation"))
				if !IsOutcomeUnknown(errRetry) || calls.Load() != 2 {
					t.Fatalf("internal retry: error=%v requests=%d", errRetry, calls.Load())
				}
			})
		}
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

func TestSubmissionGuardPreservesSafeEmptyRequestRetry(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) != 2 {
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
	for range 2 {
		response, errGet := client.Get(server.URL)
		if errGet != nil {
			t.Fatal(errGet)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}
	if calls.Load() != 3 {
		t.Fatalf("safe request not retried: calls=%d", calls.Load())
	}
}
