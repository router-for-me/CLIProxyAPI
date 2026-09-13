package claudemaster

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
)

const masterAPIHost = "api.anthropic.com"

// ProxyOptions keeps the master account's control plane separate from inference.
// Inference must honor request cancellation. Certificate is trusted by the child
// process only; this package never changes the operating system trust store.
type ProxyOptions struct {
	Certificate      tls.Certificate
	Inference        http.Handler
	ControlTransport http.RoundTripper
}

// ProxyStats is a best-effort, counter-only diagnostic snapshot. It never
// contains request paths, headers, bodies, account identities, or credentials.
// Accepted means an authenticated, valid CONNECT was handed off to a tunnel;
// APIRequests counts requests successfully parsed after the local TLS handshake.
type ProxyStats struct {
	ConnectAccepted   uint64
	ConnectRejected   uint64
	APIRequests       uint64
	InferenceRequests uint64
	ControlRequests   uint64
	BlockedRequests   uint64
	ActiveConnections uint64
}

type proxyCounters struct {
	connectAccepted   atomic.Uint64
	connectRejected   atomic.Uint64
	apiRequests       atomic.Uint64
	inferenceRequests atomic.Uint64
	controlRequests   atomic.Uint64
	blockedRequests   atomic.Uint64
}

// Proxy is a process-local CONNECT proxy, not a network security boundary.
// Only api.anthropic.com is TLS-terminated; other HTTPS hosts are blind tunnels.
type Proxy struct {
	url         string
	proxyAuth   string
	ctx         context.Context
	cancel      context.CancelFunc
	outer       *http.Server
	inner       *http.Server
	innerListen *proxyListener
	transport   *http.Transport
	tlsConfig   *tls.Config
	inference   http.Handler
	control     *httputil.ReverseProxy
	mu          sync.Mutex
	closed      bool
	conns       map[*proxyConn]struct{}
	wg          sync.WaitGroup
	closeOnce   sync.Once
	counters    proxyCounters
}

// StartProxy binds an ephemeral IPv4 loopback port. It never reads account
// credentials and never logs request URLs, bodies, headers, or transport errors.
func StartProxy(opts ProxyOptions) (*Proxy, error) {
	if opts.Inference == nil || len(opts.Certificate.Certificate) == 0 || opts.Certificate.PrivateKey == nil {
		return nil, errors.New("proxy requires an inference handler and TLS certificate")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, errors.New("cannot generate proxy authentication")
	}
	password := base64.RawURLEncoding.EncodeToString(secret)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("cannot bind local proxy")
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Proxy{
		url:       "http://claude-master:" + password + "@" + listener.Addr().String(),
		proxyAuth: "Basic " + base64.StdEncoding.EncodeToString([]byte("claude-master:"+password)), ctx: ctx, cancel: cancel,
		inference: opts.Inference, conns: make(map[*proxyConn]struct{}),
		tlsConfig: &tls.Config{Certificates: []tls.Certificate{opts.Certificate}, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}},
	}
	p.innerListen = &proxyListener{connections: make(chan net.Conn), done: make(chan struct{}), addr: listener.Addr()}
	transport := opts.ControlTransport
	if transport == nil {
		// Deliberately bypass the child's proxy environment and use normal public
		// CA verification. No deadlines terminate a connected RC/SSE stream.
		p.transport = &http.Transport{Proxy: nil, DialContext: (&net.Dialer{}).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DisableCompression: true}
		transport = p.transport
	}
	p.control = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "https"
			pr.Out.URL.Host = masterAPIHost
			pr.Out.Host = masterAPIHost
			// Rewrite mode removes forwarding and hop-by-hop headers but does
			// not parse/re-encode the original control-plane query string.
			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
		},
		Transport: transport, FlushInterval: -1,
		ErrorLog: log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "control upstream unavailable", http.StatusBadGateway)
		},
	}
	baseContext := func(net.Listener) context.Context { return ctx }
	p.outer = &http.Server{Handler: p.ownedHandler(p.handleConnect), BaseContext: baseContext, ErrorLog: log.New(io.Discard, "", 0)}
	p.inner = &http.Server{Handler: p.ownedHandler(p.handleAPI), BaseContext: baseContext, ErrorLog: log.New(io.Discard, "", 0)}
	p.wg.Add(2)
	go func() { defer p.wg.Done(); _ = p.outer.Serve(listener) }()
	go func() { defer p.wg.Done(); _ = p.inner.Serve(p.innerListen) }()
	return p, nil
}

// URL includes the per-process proxy password. Pass it only to the child process;
// do not log it or expose it in status output.
func (p *Proxy) URL() string { return p.url }

// Snapshot can be displayed without exposing the proxy's private capability or
// user data. ActiveConnections counts owned CONNECT sockets, including both ends
// of blind tunnels; it does not enumerate destinations or transport pool sockets.
func (p *Proxy) Snapshot() ProxyStats {
	p.mu.Lock()
	active := uint64(len(p.conns))
	p.mu.Unlock()
	return ProxyStats{
		ConnectAccepted:   p.counters.connectAccepted.Load(),
		ConnectRejected:   p.counters.connectRejected.Load(),
		APIRequests:       p.counters.apiRequests.Load(),
		InferenceRequests: p.counters.inferenceRequests.Load(),
		ControlRequests:   p.counters.controlRequests.Load(),
		BlockedRequests:   p.counters.blockedRequests.Load(),
		ActiveConnections: active,
	}
}

// Close cancels handlers and closes both normal and CONNECT-hijacked sockets.
// It is safe to call concurrently or more than once.
func (p *Proxy) Close() error {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.cancel()
		connections := make([]*proxyConn, 0, len(p.conns))
		for conn := range p.conns {
			connections = append(connections, conn)
		}
		p.mu.Unlock()
		_ = p.outer.Close()
		_ = p.innerListen.Close()
		_ = p.inner.Close()
		for _, conn := range connections {
			_ = conn.Close()
		}
		if p.transport != nil {
			p.transport.CloseIdleConnections()
		}
		p.wg.Wait()
	})
	return nil
}

func (p *Proxy) ownedHandler(fn http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			http.Error(w, "proxy closed", http.StatusServiceUnavailable)
			return
		}
		p.wg.Add(1)
		p.mu.Unlock()
		defer p.wg.Done()
		fn(w, r)
	})
}

func (p *Proxy) track(conn net.Conn) *proxyConn {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		_ = conn.Close()
		return nil
	}
	tracked := &proxyConn{Conn: conn, owner: p}
	p.conns[tracked] = struct{}{}
	return tracked
}

func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		p.counters.connectRejected.Add(1)
		http.Error(w, "proxy expects HTTPS CONNECT", http.StatusBadRequest)
		return
	}
	if len(r.Header.Values("Proxy-Authorization")) != 1 || subtle.ConstantTimeCompare([]byte(r.Header.Get("Proxy-Authorization")), []byte(p.proxyAuth)) != 1 {
		p.counters.connectRejected.Add(1)
		w.Header().Set("Proxy-Authenticate", `Basic realm="claude-master"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	host, err := proxyAuthority(r.RequestURI)
	if err != nil || r.Host != r.RequestURI || r.ContentLength > 0 || len(r.TransferEncoding) != 0 {
		p.counters.connectRejected.Add(1)
		http.Error(w, "invalid CONNECT authority", http.StatusBadRequest)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		p.counters.connectRejected.Add(1)
		http.Error(w, "CONNECT unavailable", http.StatusInternalServerError)
		return
	}
	raw, buffered, err := hijacker.Hijack()
	if err != nil {
		p.counters.connectRejected.Add(1)
		return
	}
	client := p.track(raw)
	if client == nil {
		p.counters.connectRejected.Add(1)
		return
	}
	p.counters.connectAccepted.Add(1)
	// Read any TLS ClientHello bytes pipelined with CONNECT before reading the
	// raw socket. The bytes must go through TLS, not into decrypted HTTP.
	stream := &proxyBufferedConn{Conn: client, reader: buffered.Reader}
	if host == masterAPIHost {
		if _, errWrite := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); errWrite != nil {
			_ = client.Close()
			return
		}
		if errFlush := buffered.Flush(); errFlush != nil {
			_ = client.Close()
			return
		}
		tlsConn := tls.Server(stream, p.tlsConfig)
		select {
		case p.innerListen.connections <- tlsConn:
		case <-p.ctx.Done():
			_ = tlsConn.Close()
		}
		return
	}
	defer func() { _ = client.Close() }()
	upstream, errDial := (&net.Dialer{}).DialContext(r.Context(), "tcp", net.JoinHostPort(host, "443"))
	if errDial != nil {
		_, _ = buffered.WriteString("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		_ = buffered.Flush()
		return
	}
	remote := p.track(upstream)
	if remote == nil {
		return
	}
	defer func() { _ = remote.Close() }()
	if _, errWrite := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); errWrite != nil {
		return
	}
	if errFlush := buffered.Flush(); errFlush != nil {
		return
	}
	finished := make(chan struct{})
	go func() {
		_, _ = io.Copy(remote, stream)
		_ = remote.Close()
		_ = client.Close()
		close(finished)
	}()
	_, _ = io.Copy(client, remote)
	_ = remote.Close()
	_ = client.Close()
	<-finished
}

func proxyAuthority(authority string) (string, error) {
	if strings.ContainsAny(authority, "/\\@?#% \t\r\n") {
		return "", errors.New("invalid authority")
	}
	host, port, err := net.SplitHostPort(authority)
	if err != nil || port != "443" || host == "" {
		return "", errors.New("HTTPS port required")
	}
	host = strings.TrimRight(strings.ToLower(host), ".")
	if ip := net.ParseIP(host); ip != nil {
		return host, nil
	}
	if len(host) > 253 {
		return "", errors.New("invalid hostname")
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("invalid hostname")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", errors.New("invalid hostname")
			}
		}
	}
	return host, nil
}

func (p *Proxy) handleAPI(w http.ResponseWriter, r *http.Request) {
	p.counters.apiRequests.Add(1)
	host := r.Host
	if !strings.Contains(host, ":") {
		host += ":443"
	}
	normalized, err := proxyAuthority(host)
	if err != nil || normalized != masterAPIHost || r.URL.IsAbs() || r.URL.Host != "" || len(r.Header.Values("Proxy-Authorization")) != 0 || !proxyCanonicalPath(r.URL) {
		p.counters.blockedRequests.Add(1)
		http.Error(w, "invalid proxied request", http.StatusBadRequest)
		return
	}
	path := r.URL.Path
	if path == "/v1/messages" || path == "/v1/messages/count_tokens" {
		if r.Method != http.MethodPost {
			p.counters.blockedRequests.Add(1)
			http.Error(w, "inference requires POST", http.StatusMethodNotAllowed)
			return
		}
		if encoding := r.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
			p.counters.blockedRequests.Add(1)
			http.Error(w, "inference requires uncompressed JSON", http.StatusUnsupportedMediaType)
			return
		}
		// Construct a fresh request, not a clone. In particular, master bearer,
		// cookies, API keys, account IDs, URL queries and TLS identity are not
		// part of the selected inference account's request. The backend removes
		// metadata.user_id while remapping the model and owns its own login.
		request, errRequest := http.NewRequestWithContext(r.Context(), http.MethodPost, path, r.Body)
		if errRequest != nil {
			p.counters.blockedRequests.Add(1)
			http.Error(w, "invalid inference request", http.StatusBadRequest)
			return
		}
		request.ContentLength = r.ContentLength
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		for _, key := range []string{"Anthropic-Version", "Anthropic-Beta"} {
			for _, value := range r.Header.Values(key) {
				request.Header.Add(key, value)
			}
		}
		p.counters.inferenceRequests.Add(1)
		p.inference.ServeHTTP(w, request)
		return
	}
	if !proxyControlPath(r.Method, path) {
		p.counters.blockedRequests.Add(1)
		// Never send an unknown/future inference endpoint to the master account.
		http.Error(w, "unrecognized control endpoint; request blocked", http.StatusForbidden)
		return
	}
	p.counters.controlRequests.Add(1)
	p.control.ServeHTTP(w, r)
}

func proxyCanonicalPath(u *url.URL) bool {
	if u.RawPath != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, "/") || strings.ContainsAny(u.Path, "\\\x00\r\n") || strings.Contains(u.Path, "//") {
		return false
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func proxyControlPath(method, path string) bool {
	if path == "/v1/environments/bridge" {
		return method == http.MethodPost
	}
	if suffix, ok := strings.CutPrefix(path, "/v1/environments/bridge/"); ok {
		return method == http.MethodDelete && proxyControlID(suffix)
	}
	if suffix, ok := strings.CutPrefix(path, "/v1/environments/"); ok {
		return proxyEnvironmentControl(method, suffix)
	}
	if path == "/v1/code/auth/refresh" || path == "/v1/oauth/token" {
		return method == http.MethodPost
	}
	if strings.HasPrefix(path, "/v1/code/runners/self-hosted/") {
		return method == http.MethodGet || method == http.MethodPost || method == http.MethodDelete
	}
	switch path {
	case "/v1/models", "/v1/mcp_servers", "/v1/code/triggers", "/v1/code/sessions", "/api/claude_code_penguin_mode", "/api/claude_cli/bootstrap", "/api/claude_code/settings", "/api/claude_code/policy_limits", "/api/claude_cli_profile", "/api/claude_cli_feedback":
		return true
	}
	for _, prefix := range []string{"/v1/code/sessions/", "/api/oauth/", "/mcp-registry/"} {
		if strings.HasPrefix(path, prefix) && len(path) > len(prefix) {
			return true
		}
	}
	return false
}

// Native Claude Code 2.1.269 uses these exact control-plane routes to acquire,
// acknowledge, renew, stop, and reconnect bridge work. Do not permit the whole
// environments subtree: an unknown endpoint must still fail closed.
func proxyEnvironmentControl(method, suffix string) bool {
	parts := strings.Split(suffix, "/")
	if (len(parts) != 3 && len(parts) != 4) || !proxyControlID(parts[0]) {
		return false
	}
	if len(parts) == 3 {
		return method == http.MethodGet && parts[1] == "work" && parts[2] == "poll" ||
			method == http.MethodPost && parts[1] == "bridge" && parts[2] == "reconnect"
	}
	if method != http.MethodPost || parts[1] != "work" || !proxyControlID(parts[2]) {
		return false
	}
	switch parts[3] {
	case "ack", "stop", "heartbeat":
		return true
	default:
		return false
	}
}

func proxyControlID(id string) bool {
	if id == "" || len(id) > 200 {
		return false
	}
	for _, char := range id {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

type proxyConn struct {
	net.Conn
	owner *Proxy
	once  sync.Once
}

func (conn *proxyConn) Close() error {
	var err error
	conn.once.Do(func() {
		err = conn.Conn.Close()
		conn.owner.mu.Lock()
		delete(conn.owner.conns, conn)
		conn.owner.mu.Unlock()
	})
	return err
}

type proxyBufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (conn *proxyBufferedConn) Read(p []byte) (int, error) { return conn.reader.Read(p) }

type proxyListener struct {
	connections chan net.Conn
	done        chan struct{}
	addr        net.Addr
	once        sync.Once
}

func (listener *proxyListener) Accept() (net.Conn, error) {
	select {
	case conn := <-listener.connections:
		return conn, nil
	case <-listener.done:
		return nil, net.ErrClosed
	}
}

func (listener *proxyListener) Close() error {
	listener.once.Do(func() { close(listener.done) })
	return nil
}

func (listener *proxyListener) Addr() net.Addr { return listener.addr }
