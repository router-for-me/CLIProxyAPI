package executor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// silentConnectProxy reproduces the production failure in Issue #6615: it
// accepts HTTP CONNECT, returns a successful tunnel response, drains the
// TLS ClientHello, and then sends no TLS bytes. closed is signaled only
// after the drain observes EOF, proving the client released that socket.
type silentConnectProxy struct {
	attempts atomic.Int32
	closed   chan struct{}
	errs     chan error

	mu      sync.Mutex
	conns   map[net.Conn]struct{}
	stopped bool
}

func startSilentConnectProxy(t *testing.T) (*silentConnectProxy, string) {
	t.Helper()
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen for silent CONNECT proxy: %v", errListen)
	}
	proxy := &silentConnectProxy{
		closed: make(chan struct{}, 16),
		errs:   make(chan error, 16),
		conns:  make(map[net.Conn]struct{}),
	}
	var handlers sync.WaitGroup
	acceptDone := make(chan struct{})
	t.Cleanup(func() {
		if errClose := listener.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close silent CONNECT proxy listener: %v", errClose)
		}
		<-acceptDone
		// A regression could leave a handshake blocked forever. Closing
		// tracked accepted sockets releases those goroutines so cleanup
		// reports the original test failure instead of hanging until the
		// whole package times out.
		proxy.closeTrackedConnections(t)
		handlers.Wait()
		proxy.mu.Lock()
		proxy.stopped = true
		proxy.mu.Unlock()
		close(proxy.errs)
		for errProxy := range proxy.errs {
			if errProxy != nil {
				t.Errorf("silent CONNECT proxy: %v", errProxy)
			}
		}
	})
	go func() {
		defer close(acceptDone)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				if !errors.Is(errAccept, net.ErrClosed) {
					proxy.reportError(fmt.Errorf("accept connection: %w", errAccept))
				}
				return
			}
			if !proxy.trackConnection(conn) {
				if errClose := conn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
					proxy.reportError(fmt.Errorf("close untracked proxy connection: %w", errClose))
				}
				continue
			}
			handlers.Add(1)
			go func() {
				defer handlers.Done()
				proxy.handle(conn)
			}()
		}
	}()
	return proxy, "http://" + listener.Addr().String()
}

func (p *silentConnectProxy) trackConnection(conn net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return false
	}
	p.conns[conn] = struct{}{}
	return true
}

func (p *silentConnectProxy) untrackConnection(conn net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.conns, conn)
}

func (p *silentConnectProxy) closeTrackedConnections(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	conns := make([]net.Conn, 0, len(p.conns))
	for conn := range p.conns {
		conns = append(conns, conn)
	}
	p.mu.Unlock()
	for _, conn := range conns {
		if errClose := conn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close tracked silent CONNECT proxy connection during cleanup: %v", errClose)
		}
	}
}

func (p *silentConnectProxy) reportError(err error) {
	// Hold mu through the non-blocking send so cleanup cannot close errs
	// between the stopped check and a late handler report.
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	select {
	case p.errs <- err:
	default:
	}
}

func (p *silentConnectProxy) handle(conn net.Conn) {
	defer func() {
		p.untrackConnection(conn)
		if errClose := conn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			p.reportError(fmt.Errorf("close proxy connection: %w", errClose))
		}
	}()
	reader := bufio.NewReader(conn)
	request, errRead := http.ReadRequest(reader)
	if errRead != nil {
		p.reportError(fmt.Errorf("read CONNECT request: %w", errRead))
		return
	}
	if request.Method != http.MethodConnect {
		p.reportError(fmt.Errorf("proxy method = %q, want CONNECT", request.Method))
		return
	}
	if _, errWrite := io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n"); errWrite != nil {
		p.reportError(fmt.Errorf("write CONNECT response: %w", errWrite))
		return
	}
	header := make([]byte, 5)
	if _, errRead := io.ReadFull(reader, header); errRead != nil {
		p.reportError(fmt.Errorf("read ClientHello header: %w", errRead))
		return
	}
	if header[0] != 22 {
		p.reportError(fmt.Errorf("first TLS record type = %d, want handshake (22)", header[0]))
		return
	}
	recordLength := int(header[3])<<8 | int(header[4])
	if _, errRead := io.ReadFull(reader, make([]byte, recordLength)); errRead != nil {
		p.reportError(fmt.Errorf("read ClientHello body: %w", errRead))
		return
	}
	p.attempts.Add(1)
	if _, errCopy := io.Copy(io.Discard, reader); errCopy != nil && !errors.Is(errCopy, net.ErrClosed) {
		p.reportError(fmt.Errorf("drain silent TLS connection: %w", errCopy))
		return
	}
	p.closed <- struct{}{}
}

// TestClaudeExecutorManagerRetriesSilentTLSHandshakeThroughConnectProxy is
// the end-to-end Issue #6615 regression: a real CONNECT proxy stays silent
// after ClientHello, the real Claude executor and shared uTLS client fail
// each handshake at the configured one-second bound, and Manager performs
// the initial attempt plus all three request-retry rounds on one credential.
func TestClaudeExecutorManagerRetriesSilentTLSHandshakeThroughConnectProxy(t *testing.T) {
	proxy, proxyURL := startSilentConnectProxy(t)

	cfg := &config.Config{}
	cfg.ProxyURL = proxyURL
	cfg.TLSHandshakeTimeoutSeconds = 1

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(3, 0, 0)
	manager.RegisterExecutor(NewClaudeExecutor(cfg))

	authID := uuid.NewString() + "-silent-tls-handshake"
	model := "claude-sonnet-4-6"
	auth := &cliproxyauth.Auth{
		ID:       authID,
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "sk-ant-api03-silent-handshake-test",
			"base_url": "https://api.anthropic.com",
		},
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	payload := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}],"max_tokens":8}`)
	executeDone := make(chan error, 1)
	go func() {
		_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{
			Model:   model,
			Payload: payload,
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
		executeDone <- errExecute
	}()

	var errExecute error
	select {
	case errExecute = <-executeDone:
	case <-time.After(15 * time.Second):
		t.Fatal("Manager.Execute did not finish after four bounded TLS handshakes")
	}
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want final configured TLS handshake timeout")
	}
	if !cliproxyexecutor.IsConfiguredTLSHandshakeTimeoutError(errExecute) {
		t.Fatalf("Execute() error = %v, want typed configured TLS handshake timeout", errExecute)
	}
	if attempts := proxy.attempts.Load(); attempts != 4 {
		t.Fatalf("silent proxy handshake attempts = %d, want 4", attempts)
	}
	for attempt := 1; attempt <= 4; attempt++ {
		select {
		case <-proxy.closed:
		case <-time.After(5 * time.Second):
			t.Fatalf("silent proxy did not observe failed socket %d being closed", attempt)
		}
	}

	updatedAuth, ok := manager.GetByID(authID)
	if !ok || updatedAuth == nil {
		t.Fatal("registered auth not found after handshake retries")
	}
	if updatedAuth.Unavailable || !updatedAuth.NextRetryAfter.IsZero() {
		t.Fatalf("configured handshake timeout cooled the credential: unavailable=%t next_retry_after=%v", updatedAuth.Unavailable, updatedAuth.NextRetryAfter)
	}
	if state := updatedAuth.ModelStates[model]; state != nil && (state.Unavailable || !state.NextRetryAfter.IsZero()) {
		t.Fatalf("configured handshake timeout cooled the model: %#v", state)
	}
}
