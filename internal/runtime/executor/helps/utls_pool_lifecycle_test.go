package helps

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func chromePoolLifecycleGate() (<-chan struct{}, func()) {
	gate := make(chan struct{})
	var once sync.Once
	return gate, func() { once.Do(func() { close(gate) }) }
}

func readChromePoolPrefix(t *testing.T, resp *http.Response, want string) {
	t.Helper()
	data := make([]byte, len(want))
	if _, err := io.ReadFull(resp.Body, data); err != nil {
		t.Fatalf("read response prefix: %v", err)
	}
	if string(data) != want {
		t.Fatalf("response prefix = %q, want %q", data, want)
	}
}

func consumeChromePoolText(t *testing.T, resp *http.Response, want string) {
	t.Helper()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close response: %v", err)
	}
	if string(data) != want {
		t.Fatalf("response = %q, want %q", data, want)
	}
}

func TestChromePoolConcurrentCancellationIsolated(t *testing.T) {
	for _, protocol := range []string{"h2", "http/1.1", ""} {
		t.Run(protocol, func(t *testing.T) {
			released, release := chromePoolLifecycleGate()
			canceledFinished := make(chan struct{})
			server, transport, connections := newChromePoolTestServer(t, protocol, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				switch r.URL.Path {
				case "/cancel":
					defer close(canceledFinished)
					_, _ = io.WriteString(w, "cancel-prefix")
					w.(http.Flusher).Flush()
					select {
					case <-r.Context().Done():
					case <-released:
					}
				case "/survive":
					_, _ = io.WriteString(w, "survivor-prefix")
					w.(http.Flusher).Flush()
					select {
					case <-r.Context().Done():
						return
					case <-released:
					}
					_, _ = io.WriteString(w, "survivor-tail")
				default:
					_, _ = io.WriteString(w, "reused")
				}
			}))
			t.Cleanup(release)
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			canceled := chromePoolRequest(t, ctx, transport, server.URL+"/cancel")
			t.Cleanup(func() {
				release()
				_ = canceled.Body.Close()
			})
			readChromePoolPrefix(t, canceled, "cancel-prefix")
			survivor := chromePoolRequest(t, t.Context(), transport, server.URL+"/survive")
			t.Cleanup(func() {
				release()
				_ = survivor.Body.Close()
			})
			readChromePoolPrefix(t, survivor, "survivor-prefix")
			if got := connections.Load(); got != 2 {
				t.Fatalf("physical TLS connections with two active streams = %d, want 2", got)
			}

			cancel()
			if _, err := io.ReadAll(canceled.Body); err == nil {
				t.Fatal("canceled response read succeeded, want an error")
			}
			_ = canceled.Body.Close()
			<-canceledFinished
			release()
			consumeChromePoolText(t, survivor, "survivor-tail")
			consumeChromePoolText(t, chromePoolRequest(t, t.Context(), transport, server.URL+"/reuse"), "reused")
			if got := connections.Load(); got != 2 {
				t.Fatalf("physical TLS connections after survivor reuse = %d, want 2", got)
			}
		})
	}
}

func TestChromePoolOldContextAndBodyCannotCloseReusedConnection(t *testing.T) {
	for _, protocol := range []string{"h2", "http/1.1", ""} {
		t.Run(protocol, func(t *testing.T) {
			released, release := chromePoolLifecycleGate()
			server, transport, connections := newChromePoolTestServer(t, protocol, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if r.URL.Path != "/stream" {
					_, _ = io.WriteString(w, "complete")
					return
				}
				_, _ = io.WriteString(w, "prefix")
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					return
				case <-released:
				}
				_, _ = io.WriteString(w, "tail")
			}))
			t.Cleanup(release)
			oldContext, cancelOld := context.WithCancel(t.Context())
			t.Cleanup(cancelOld)
			old := chromePoolRequest(t, oldContext, transport, server.URL+"/first")
			consumeChromePoolText(t, old, "complete")
			active := chromePoolRequest(t, t.Context(), transport, server.URL+"/stream")
			t.Cleanup(func() {
				release()
				_ = active.Body.Close()
			})
			readChromePoolPrefix(t, active, "prefix")
			if got := connections.Load(); got != 1 {
				t.Fatalf("physical TLS connections before old cancellation = %d, want 1", got)
			}

			cancelOld()
			if err := old.Body.Close(); err != nil {
				t.Fatalf("repeat close of old response: %v", err)
			}
			release()
			consumeChromePoolText(t, active, "tail")
			consumeChromePoolText(t, chromePoolRequest(t, t.Context(), transport, server.URL+"/last"), "complete")
			if got := connections.Load(); got != 1 {
				t.Fatalf("physical TLS connections after old cancellation and close = %d, want 1", got)
			}
		})
	}
}

func TestChromePoolEarlyClosePreservesProtocolIsolation(t *testing.T) {
	for _, protocol := range []string{"h2", "http/1.1", ""} {
		t.Run(protocol, func(t *testing.T) {
			released, release := chromePoolLifecycleGate()
			server, transport, connections := newChromePoolTestServer(t, protocol, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if r.URL.Path != "/stream" {
					_, _ = io.WriteString(w, "complete")
					return
				}
				_, _ = io.WriteString(w, "prefix")
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
				case <-released:
				}
			}))
			t.Cleanup(release)
			resp := chromePoolRequest(t, t.Context(), transport, server.URL+"/stream")
			t.Cleanup(func() {
				release()
				_ = resp.Body.Close()
			})
			readChromePoolPrefix(t, resp, "prefix")
			if err := resp.Body.Close(); err != nil {
				t.Fatalf("close unfinished response: %v", err)
			}
			release()
			consumeChromePoolText(t, chromePoolRequest(t, t.Context(), transport, server.URL+"/next"), "complete")
			wantConnections := int32(2)
			if protocol == "h2" {
				// Closing the body resets only this HTTP/2 stream, preserving
				// the connection for the next request without leaking bytes.
				wantConnections = 1
			}
			if got := connections.Load(); got != wantConnections {
				t.Fatalf("physical TLS connections after early close = %d, want %d", got, wantConnections)
			}
		})
	}
}

func TestChromePoolReadFailureDiscardsConnection(t *testing.T) {
	for _, protocol := range []string{"h2", "http/1.1", ""} {
		t.Run(protocol, func(t *testing.T) {
			server, transport, connections := newChromePoolTestServer(t, protocol, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if r.URL.Path == "/broken" {
					w.Header().Set("Content-Length", "20")
					_, _ = io.WriteString(w, "short")
					w.(http.Flusher).Flush()
					return
				}
				_, _ = io.WriteString(w, "complete")
			}))
			broken := chromePoolRequest(t, t.Context(), transport, server.URL+"/broken")
			t.Cleanup(func() { _ = broken.Body.Close() })
			body, err := io.ReadAll(broken.Body)
			if err == nil {
				t.Fatal("truncated response read succeeded, want an error")
			}
			if string(body) != "short" {
				t.Fatalf("truncated response = %q, want short", body)
			}
			_ = broken.Body.Close()
			consumeChromePoolText(t, chromePoolRequest(t, t.Context(), transport, server.URL+"/next"), "complete")
			if got := connections.Load(); got != 2 {
				t.Fatalf("physical TLS connections after read failure = %d, want 2", got)
			}
		})
	}
}

func TestChromePoolHTTP1PostFailureNeverReplayed(t *testing.T) {
	for _, test := range []struct {
		name      string
		getBody   bool
		headerKey string
	}{
		{name: "post"},
		{name: "get_body", getBody: true},
		{name: "idempotency_key", getBody: true, headerKey: "Idempotency-Key"},
		{name: "x_idempotency_key", getBody: true, headerKey: "X-Idempotency-Key"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var attempts atomic.Int32
			server, transport, connections := newChromePoolTestServer(t, "http/1.1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != "offer" {
					t.Errorf("request body = %q, error = %v", body, err)
				}
				if r.URL.Path == "/warm" {
					_, _ = io.WriteString(w, "complete")
					return
				}
				if test.headerKey != "" && r.Header.Get(test.headerKey) != "request-key" {
					t.Errorf("%s header was not preserved", test.headerKey)
				}
				if attempts.Add(1) != 1 {
					_, _ = io.WriteString(w, "unexpected replay")
					return
				}
				conn, _, errHijack := w.(http.Hijacker).Hijack()
				if errHijack != nil {
					t.Errorf("hijack failed response: %v", errHijack)
					return
				}
				_ = conn.Close()
			}))
			consumeChromePoolText(t, chromePoolRequest(t, t.Context(), transport, server.URL+"/warm"), "complete")
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/fail", strings.NewReader("offer"))
			if err != nil {
				t.Fatal(err)
			}
			var getBodyCalls atomic.Int32
			if test.getBody {
				req.GetBody = func() (io.ReadCloser, error) {
					getBodyCalls.Add(1)
					return io.NopCloser(strings.NewReader("offer")), nil
				}
			} else {
				req.GetBody = nil
			}
			if test.headerKey != "" {
				req.Header.Set(test.headerKey, "request-key")
			}
			resp, err := transport.RoundTrip(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			if err == nil {
				t.Fatal("failed POST succeeded, want the original connection failure")
			}
			if got := attempts.Load(); got != 1 {
				t.Errorf("failed POST attempts = %d, want 1", got)
			}
			if got := getBodyCalls.Load(); got != 0 {
				t.Errorf("GetBody calls = %d, want 0", got)
			}
			if got := connections.Load(); got != 1 {
				t.Errorf("physical TLS connections after POST failure = %d, want 1", got)
			}
		})
	}
}

func TestChromePoolRetirementDrainsActiveStream(t *testing.T) {
	for _, protocol := range []string{"h2", "http/1.1", ""} {
		t.Run(protocol, func(t *testing.T) {
			released, release := chromePoolLifecycleGate()
			server, transport, connections := newChromePoolTestServer(t, protocol, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if r.URL.Path != "/stream" {
					_, _ = io.WriteString(w, "complete")
					return
				}
				_, _ = io.WriteString(w, "prefix")
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					return
				case <-released:
				}
				_, _ = io.WriteString(w, "tail")
			}))
			t.Cleanup(release)
			active := chromePoolRequest(t, t.Context(), transport, server.URL+"/stream")
			t.Cleanup(func() {
				release()
				_ = active.Body.Close()
			})
			readChromePoolPrefix(t, active, "prefix")
			transport.retire()
			transport.retire()
			release()
			consumeChromePoolText(t, active, "tail")
			for range 2 {
				consumeChromePoolText(t, chromePoolRequest(t, t.Context(), transport, server.URL+"/after"), "complete")
			}
			if got := connections.Load(); got != 3 {
				t.Fatalf("physical TLS connections after retirement = %d, want 3", got)
			}
		})
	}
}
