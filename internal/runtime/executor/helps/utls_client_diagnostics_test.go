package helps

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestUtlsRoundTripLogsTransportFailure(t *testing.T) {
	// The server completes the TLS handshake, then closes without answering.
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, _, errHijack := w.(http.Hijacker).Hijack()
		if errHijack != nil {
			t.Error(errHijack)
			return
		}
		if errClose := conn.Close(); errClose != nil {
			t.Error(errClose)
		}
	}))
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	serverURL, errParse := url.Parse(server.URL)
	if errParse != nil {
		t.Fatal(errParse)
	}

	hook := logtest.NewGlobal()
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(make(log.LevelHooks)) })

	roundTripper := &utlsRoundTripper{rootCAs: roots, dialer: contextDialerFunc(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	})}
	req, errReq := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL, strings.NewReader("payload-secret"))
	if errReq != nil {
		t.Fatal(errReq)
	}
	req.Header.Set("Authorization", "Bearer token-secret")

	resp, errRoundTrip := roundTripper.RoundTrip(req)
	if errRoundTrip == nil {
		_ = resp.Body.Close()
		t.Fatal("RoundTrip succeeded, want a transport failure")
	}

	var warnings []*log.Entry
	for _, entry := range hook.AllEntries() {
		if entry.Level == log.WarnLevel {
			warnings = append(warnings, entry)
		}
	}
	if len(warnings) != 1 {
		t.Fatalf("warn lines = %d, want 1", len(warnings))
	}
	rendered, errFormat := (&logging.LogFormatter{}).Format(warnings[0])
	if errFormat != nil {
		t.Fatal(errFormat)
	}
	line := string(rendered) // what the gateway actually prints
	for _, want := range []string{
		"host=" + serverURL.Hostname(), "proxy=direct", "alpn=http/1.1",
		"dial_ms=", "tls_ms=", "app_bytes_read=0", "request_bytes=14",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("log line %q is missing %q", line, want)
		}
	}
	if strings.Contains(line, "app_bytes_written=0") || !strings.Contains(line, "app_bytes_written=") {
		t.Errorf("log line %q should report the bytes written", line)
	}
	if !strings.Contains(line, "error=") {
		t.Errorf("log line %q is missing the error", line)
	}
	if strings.Contains(line, "secret") {
		t.Errorf("log line leaks request data: %q", line)
	}
}

func TestUtlsLogFailureNeutralizesControlCharacters(t *testing.T) {
	hook := logtest.NewGlobal()
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(make(log.LevelHooks)) })

	roundTripper := &utlsRoundTripper{proxyLabel: "socks5://proxy\r\n[INFO] forged"}
	req, errReq := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.test", nil)
	if errReq != nil {
		t.Fatal(errReq)
	}
	roundTripper.logFailure(req, "host\n[INFO] forged", time.Now(), &utlsDiagnostics{}, errors.New("upstream said\n[INFO] forged"))

	rendered, errFormat := (&logging.LogFormatter{}).Format(hook.LastEntry())
	if errFormat != nil {
		t.Fatal(errFormat)
	}
	if got := strings.Count(string(rendered), "\n"); got != 1 {
		t.Fatalf("rendered log has %d line breaks, want 1: %q", got, rendered)
	}
	// The error is reduced to allowlisted signals: no free-form peer text.
	if strings.Contains(string(rendered), "upstream said") {
		t.Fatalf("rendered log carries free-form error text: %q", rendered)
	}
}

func TestUtlsRoundTripDoesNotLogCanceledRequest(t *testing.T) {
	hook := logtest.NewGlobal()
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(make(log.LevelHooks)) })

	roundTripper := &utlsRoundTripper{dialer: contextDialerFunc(func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	})}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, "https://127.0.0.1:1", nil)
	if errReq != nil {
		t.Fatal(errReq)
	}
	if _, errRoundTrip := roundTripper.RoundTrip(req); errRoundTrip == nil {
		t.Fatal("RoundTrip succeeded on a canceled request")
	}
	for _, entry := range hook.AllEntries() {
		if entry.Level == log.WarnLevel {
			t.Fatalf("canceled request logged a transport failure: %s", entry.Message)
		}
	}
}

// The diagnostics wrap the TLS connection in countedConn; both ALPN paths must still work over it.
func TestUtlsRoundTripOverCountedConn(t *testing.T) {
	for _, protocol := range []string{"h2", "http/1.1"} {
		t.Run(protocol, func(t *testing.T) {
			tlsConn, _, serverURL := newALPNTestConnection(t, protocol, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if body, errRead := io.ReadAll(r.Body); errRead != nil || string(body) != "offer" {
					t.Errorf("request body = %q, error = %v", body, errRead)
				}
				_, _ = io.WriteString(w, "answer")
			}))
			counted := &countedConn{Conn: tlsConn}
			req, errRequest := http.NewRequestWithContext(t.Context(), http.MethodPost, serverURL, strings.NewReader("offer"))
			if errRequest != nil {
				t.Fatal(errRequest)
			}
			resp, errRoundTrip := roundTripUtlsConnectionOnConn(req, tlsConn, counted)
			if errRoundTrip != nil {
				t.Fatal(errRoundTrip)
			}
			body, errRead := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if errRead != nil || string(body) != "answer" {
				t.Fatalf("response body = %q, error = %v", body, errRead)
			}
			if counted.written.Load() == 0 || counted.read.Load() == 0 {
				t.Fatalf("counted bytes written=%d read=%d, want both > 0", counted.written.Load(), counted.read.Load())
			}
		})
	}
}
