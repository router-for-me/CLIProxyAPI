package claudemaster

import (
	"bufio"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// serve can listen on [::1]:8443, and Go then names the far end "[::1]:port". It must still be a counted,
// logged handshake failure, not a debug-only notice.
func TestHandshakeFailuresFromIPv6AndIPv4ClientsAreBothCountedAndNamed(t *testing.T) {
	logs := captureLogs(t, "info")
	var counted atomic.Int32
	w := newHandshakeErrorWriter(func() { counted.Add(1) })
	for _, line := range []string{
		"http: TLS handshake error from 127.0.0.1:50000: remote error: tls: bad certificate\n",
		"http: TLS handshake error from [::1]:50001: tls: client didn't provide a certificate\n",
		"http: TLS handshake error from [2001:db8::7]:50002: EOF\n",
	} {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if counted.Load() != 3 {
		t.Fatalf("%d of 3 handshake failures were counted", counted.Load())
	}
	out := logs.String()
	for _, want := range []string{"remote=127.0.0.1", "remote=\"::1\"", "remote=\"2001:db8::7\""} {
		if !strings.Contains(out, want) && !strings.Contains(out, strings.ReplaceAll(want, "\"", "")) {
			t.Errorf("%s missing from the log:\n%s", want, out)
		}
	}
	if strings.Contains(out, "50001") || strings.Contains(out, "[::1]") {
		t.Errorf("a port or brackets leaked into the remote:\n%s", out)
	}
	if strings.Count(out, "client TLS handshake failed") != 3 {
		t.Errorf("each remote gets its own warning:\n%s", out)
	}
}

// The metric and the proxy's own counter must agree on rejected connections, whichever way they were rejected.
func TestEveryRejectedConnectionIsInTheConnectionMetric(t *testing.T) {
	reader := startMetrics(t, nil)
	p, _, _ := proxyTestStart(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), proxyTestTransport(func(r *http.Request) (*http.Response, error) {
		return proxyTestResponse(200, `{}`), nil
	}))
	for _, request := range []string{
		"GET /not-a-connect HTTP/1.1\r\nHost: example.invalid\r\n\r\n",                // not CONNECT
		"CONNECT api.anthropic.com:80 HTTP/1.1\r\nHost: api.anthropic.com:80\r\n\r\n", // not an acceptable authority
	} {
		conn, err := proxyTestDialRaw(p)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(conn, request)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		_ = conn.Close()
	}
	rejected := p.Snapshot().ConnectRejected
	if rejected != 2 {
		t.Fatalf("the proxy counted %d rejections, want 2", rejected)
	}
	if got := sumOf(collect(t, reader), "claude_master.proxy.connections", map[string]string{"result": "rejected"}); got != int64(rejected) {
		t.Fatalf("the metric counted %d rejected connections, the proxy %d", got, rejected)
	}
}
