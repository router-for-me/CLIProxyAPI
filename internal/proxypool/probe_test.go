package proxypool

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// connectProxy is a minimal HTTP CONNECT proxy backed by a real listener.
type connectProxy struct {
	listener    net.Listener
	connections chan string
}

func newConnectProxy(t *testing.T) *connectProxy {
	t.Helper()
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("net.Listen() error = %v", errListen)
	}
	proxy := &connectProxy{listener: listener, connections: make(chan string, 4)}
	t.Cleanup(func() {
		if errClose := listener.Close(); errClose != nil && !strings.Contains(errClose.Error(), "closed network connection") {
			t.Errorf("listener.Close() error = %v", errClose)
		}
	})
	go proxy.serve()
	return proxy
}

func (p *connectProxy) addr() string {
	return p.listener.Addr().String()
}

func (p *connectProxy) serve() {
	for {
		conn, errAccept := p.listener.Accept()
		if errAccept != nil {
			return
		}
		go p.handle(conn)
	}
}

func (p *connectProxy) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	req, errRead := http.ReadRequest(bufio.NewReader(conn))
	if errRead != nil {
		return
	}
	if req.Method != http.MethodConnect {
		_, _ = io.WriteString(conn, "HTTP/1.1 405 Method Not Allowed\r\n\r\n")
		return
	}
	upstream, errDial := net.DialTimeout("tcp", req.Host, 5*time.Second)
	if errDial != nil {
		_, _ = io.WriteString(conn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer func() { _ = upstream.Close() }()
	select {
	case p.connections <- req.Host:
	default:
	}
	if _, errWrite := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); errWrite != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	_ = upstream.SetDeadline(time.Now().Add(5 * time.Second))
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, conn)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, upstream)
		done <- struct{}{}
	}()
	<-done
}

func newProxyProbeManager(t *testing.T, proxyURL, testURL string, timeout time.Duration) *Manager {
	t.Helper()
	manager, errNew := NewManager([]Config{{
		ID:       "pool",
		Strategy: StrategySticky,
		Nodes:    []string{proxyURL},
		HealthCheck: HealthCheck{
			Enabled:  true,
			TestURL:  testURL,
			Interval: time.Minute,
			Timeout:  timeout,
		},
	}}, WithFailureThreshold(1))
	if errNew != nil {
		t.Fatalf("NewManager() error = %v", errNew)
	}
	t.Cleanup(manager.Close)
	return manager
}

func TestDefaultProbeThroughHTTPProxyToTLSServer(t *testing.T) {
	t.Parallel()

	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	proxy := newConnectProxy(t)
	manager := newProxyProbeManager(t, "http://"+proxy.addr(), target.URL, 2*time.Second)
	manager.ProbePool(context.Background(), "pool")

	status := mustStatus(t, manager, "pool")
	if !status[0].Healthy {
		t.Fatalf("TLS target probe marked node unhealthy: %+v", status[0])
	}
	parsed, errURL := url.Parse(target.URL)
	if errURL != nil {
		t.Fatalf("url.Parse() error = %v", errURL)
	}
	select {
	case host := <-proxy.connections:
		if host != parsed.Host {
			t.Fatalf("proxy CONNECT host = %q, want %q", host, parsed.Host)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("proxy did not receive a CONNECT request")
	}
}

func TestDefaultProbeThroughHTTPProxyToHTTPServer(t *testing.T) {
	t.Parallel()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	proxy := newConnectProxy(t)
	manager := newProxyProbeManager(t, "http://"+proxy.addr(), target.URL, 2*time.Second)
	manager.ProbePool(context.Background(), "pool")

	status := mustStatus(t, manager, "pool")
	if !status[0].Healthy {
		t.Fatalf("HTTP target probe marked node unhealthy: %+v", status[0])
	}
}

func TestDefaultProbeMarksUnreachableProxyUnhealthy(t *testing.T) {
	t.Parallel()

	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("net.Listen() error = %v", errListen)
	}
	closedAddr := listener.Addr().String()
	if errClose := listener.Close(); errClose != nil {
		t.Fatalf("listener.Close() error = %v", errClose)
	}

	manager := newProxyProbeManager(t, "http://"+closedAddr, "https://example.com", 200*time.Millisecond)
	manager.ProbePool(context.Background(), "pool")

	status := mustStatus(t, manager, "pool")
	if status[0].Healthy {
		t.Fatalf("unreachable proxy node remained healthy: %+v", status[0])
	}
}

func TestProbeTargetDefaultsAndValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "https default port", input: "https://example.com/path", want: "example.com:443"},
		{name: "http default port", input: "http://example.com", want: "example.com:80"},
		{name: "explicit port", input: "https://example.com:8443", want: "example.com:8443"},
		{name: "empty uses default", input: "", want: "www.google.com:443"},
		{name: "missing host", input: "https://", wantErr: true},
		{name: "unsupported scheme", input: "ftp://example.com", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, errTarget := probeTarget(tc.input)
			if tc.wantErr {
				if errTarget == nil {
					t.Fatalf("probeTarget(%q) = %q, want error", tc.input, got)
				}
				return
			}
			if errTarget != nil {
				t.Fatalf("probeTarget(%q) error = %v", tc.input, errTarget)
			}
			if got != tc.want {
				t.Fatalf("probeTarget(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestDefaultProbeRejectsDirectDialerMode(t *testing.T) {
	t.Parallel()

	errProbe := defaultProbe(context.Background(), "direct", "https://example.com", time.Second)
	if errProbe == nil {
		t.Fatal("defaultProbe() = nil error for direct mode")
	}
	if !strings.Contains(fmt.Sprint(errProbe), "concrete proxy") {
		t.Fatalf("defaultProbe() error = %v, want concrete proxy rejection", errProbe)
	}
}
