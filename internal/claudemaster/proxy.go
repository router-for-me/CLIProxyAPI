package claudemaster

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const masterAPIHost = "api.anthropic.com"

// ProxyOptions keeps the master account's control plane separate from inference.
// Inference must honor request cancellation. Certificate is trusted by the child
// process only; this package never changes the operating system trust store.
type ProxyOptions struct {
	Certificate      tls.Certificate
	GetCertificate   func(*tls.ClientHelloInfo) (*tls.Certificate, error)
	Inference        http.Handler
	ControlTransport http.RoundTripper

	// The proxy's own listener is TLS and requires a client certificate signed by ClientCAs before it
	// reads a single request: there is no password. ProxyCertificate is what it presents to clients.
	ProxyCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error)
	ClientCAs        *x509.CertPool
	// Listen is the address to bind; the default is an ephemeral IPv4 loopback port.
	Listen string
	// OpenListen, when set, adds a SECOND listener that is plain HTTP and asks for no certificate.
	// It must be a loopback address: whatever can reach it is trusted, so an authenticating tunnel
	// (cloudflared, an SSH forward) is expected to be the only thing that does.
	OpenListen string
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
	openAddr    string
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
	draining    bool         // Drain has begun
	balanced    bool         // a load balancer stops sending new connections here once a rollout begins
	running     atomic.Int64 // inference requests being served (the listeners are in wg, not here)
	conns       map[*proxyConn]struct{}
	wg          sync.WaitGroup
	closeOnce   sync.Once
	counters    proxyCounters

	clients sync.Map // inner *tls.Conn -> the client's name (see proxy_observe.go)
	routes  sync.Map // route shapes already logged by noteRoute
	seen    sync.Map // client name -> first connection logged
}

// StartProxy binds an ephemeral IPv4 loopback port. It never reads account
// credentials and never logs request URLs, bodies, headers, or transport errors.
func StartProxy(opts ProxyOptions) (*Proxy, error) {
	if opts.Inference == nil || opts.GetCertificate == nil && (len(opts.Certificate.Certificate) == 0 || opts.Certificate.PrivateKey == nil) {
		return nil, errors.New("proxy requires an inference handler and TLS certificate")
	}
	if opts.ProxyCertificate == nil || opts.ClientCAs == nil {
		return nil, errors.New("proxy requires its own certificate and the client CAs it accepts")
	}
	network, address := "tcp4", "127.0.0.1:0"
	if opts.Listen != "" {
		network, address = "tcp", opts.Listen
	}
	var openListener net.Listener
	if opts.OpenListen != "" {
		if _, _, err := loopbackEndpoint(opts.OpenListen); err != nil {
			return nil, fmt.Errorf("the open listener: %w", err)
		}
	}
	rawListener, err := net.Listen(network, address)
	if err != nil {
		return nil, errors.New("cannot bind the proxy")
	}
	if opts.OpenListen != "" {
		openListener, err = net.Listen("tcp", opts.OpenListen)
		if err != nil {
			_ = rawListener.Close()
			return nil, errors.New("cannot bind the open loopback listener")
		}
	}
	listener := tls.NewListener(rawListener, &tls.Config{
		GetCertificate: opts.ProxyCertificate, ClientCAs: opts.ClientCAs, ClientAuth: tls.RequireAndVerifyClientCert,
		MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"},
	})
	ctx, cancel := context.WithCancel(context.Background())
	p := &Proxy{
		url: "https://" + rawListener.Addr().String(), ctx: ctx, cancel: cancel,
		inference: opts.Inference, conns: make(map[*proxyConn]struct{}),
		tlsConfig: &tls.Config{Certificates: []tls.Certificate{opts.Certificate}, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}},
	}
	if opts.GetCertificate != nil {
		// Empty Certificates makes Go invoke the provider even without SNI.
		p.tlsConfig.Certificates = nil
		p.tlsConfig.GetCertificate = opts.GetCertificate
	}
	p.innerListen = &proxyListener{connections: make(chan net.Conn), done: make(chan struct{}), addr: rawListener.Addr()}
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
	p.outer = &http.Server{Handler: p.ownedHandler(p.handleConnect), BaseContext: baseContext, ErrorLog: log.New(newHandshakeErrorWriter(observeHandshakeError), "", 0)}
	p.inner = &http.Server{
		Handler: p.ownedHandler(p.handleAPI), BaseContext: baseContext, ErrorLog: log.New(io.Discard, "", 0),
		// Every request on an inner connection knows which client opened the tunnel, and for which
		// project when the tunnel named one.
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if v, ok := p.clients.Load(c); ok {
				tunnel := v.(tunnelOwner)
				ctx = context.WithValue(ctx, clientCtxKey{}, tunnel.client)
				if tunnel.project != "" {
					ctx = context.WithValue(ctx, projectCtxKey{}, tunnel.project)
				}
			}
			return ctx
		},
		ConnState: func(c net.Conn, state http.ConnState) {
			if state == http.StateClosed || state == http.StateHijacked {
				p.clients.Delete(c)
			}
		},
	}
	p.wg.Add(2)
	go func() { defer p.wg.Done(); _ = p.outer.Serve(listener) }()
	if openListener != nil {
		p.openAddr = openListener.Addr().String()
		p.wg.Add(1)
		go func() { defer p.wg.Done(); _ = p.outer.Serve(openListener) }()
	}
	go func() { defer p.wg.Done(); _ = p.inner.Serve(p.innerListen) }()
	lg().Info("proxy listening", "addr", p.Addr(), "open_loopback_addr", p.openAddr)
	registerStateSource(p)
	return p, nil
}

// URL is the proxy's https address. It carries no credential: clients authenticate with a certificate.
func (p *Proxy) URL() string { return p.url }

// OpenAddr is the plain loopback listener's address, or "" when there is none.
func (p *Proxy) OpenAddr() string { return p.openAddr }

// Addr is the address the proxy listens on.
func (p *Proxy) Addr() string { return strings.TrimPrefix(p.url, "https://") }

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

// beginInference admits an inference request unless the proxy is closed, or draining without a
// balancer in front. Behind a balancer a draining proxy keeps serving the connections it has (the
// balancer sends new ones elsewhere), and each response closes its connection (handleAPI), so the
// client's next request opens a new one through the balancer.
func (p *Proxy) beginInference() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || (p.draining && !p.balanced) {
		return false
	}
	p.running.Add(1)
	return true
}

// setBalanced says a load balancer is in front (serve --balanced).
func (p *Proxy) setBalanced(balanced bool) {
	p.mu.Lock()
	p.balanced = balanced
	p.mu.Unlock()
}

func (p *Proxy) isDraining() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.draining
}

func (p *Proxy) endInference() { p.running.Add(-1) }

// writeProxyRestarting answers an inference request that arrives while the server stops: what
// Anthropic answers when it cannot take a request now (529 overloaded_error, x-should-retry), so
// Claude Code backs off and retries, and finds the server again once it is back.
func writeProxyRestarting(w http.ResponseWriter) {
	w.Header().Set("X-Should-Retry", "true")
	w.Header().Set("Connection", "close")
	backendAnthropicError(w, 529, "overloaded_error", "claude-master is restarting; retry")
}

// Drain lets the requests already running finish before Close cancels them, and returns when
// none is running or timeout passes, whichever is first. Without a balancer, new inference requests
// are answered with a retryable 529 meanwhile; with one (serve --balanced), requests on the
// connections this proxy already has are served and each closes its connection, so the client
// moves to the other server without an error. Every response during a drain carries
// Connection: close. Before this a restart cancelled every running request at once and the session
// got a 499 it does not retry (2026-10-10 05:45).
func (p *Proxy) Drain(timeout time.Duration) {
	p.mu.Lock()
	if p.closed || p.draining {
		p.mu.Unlock()
		return
	}
	p.draining = true
	balanced := p.balanced
	p.mu.Unlock()
	if balanced {
		// Close every idle keep-alive connection now, and every busy one after its response: a client's next
		// request then opens a new connection, which the balancer sends to the server taking over. Without this
		// each client's first request after a rollout still landed here, on the old configuration: on 2026-10-10
		// the old server, which did not know a subscription the new one had just added, sent 30 requests to an
		// API-key backup with no credit while the new server had capacity.
		p.inner.SetKeepAlivesEnabled(false)
	}
	running := p.running.Load()
	if running == 0 && !balanced {
		lg().Info("proxy draining: no request running")
		return
	}
	start := time.Now()
	lg().Info("proxy draining", "running", running, "balanced", balanced, "timeout", timeout)
	// Polled, not waited on: behind a balancer requests still start while draining. Two quiet
	// samples in a row end it; a request that starts between the last sample and Close is
	// cancelled, and a cancelled request is answered with a retryable 529.
	deadline := start.Add(timeout)
	quiet := 0
	for {
		if p.running.Load() == 0 {
			quiet++
			if quiet >= 2 {
				lg().Info("proxy drained", "waited", time.Since(start).Round(time.Millisecond))
				return
			}
		} else {
			quiet = 0
		}
		if !time.Now().Before(deadline) {
			lg().Warn("proxy drain timed out; the requests still running are cancelled", "running", p.running.Load())
			return
		}
		time.Sleep(drainPoll)
	}
}

var drainPoll = 25 * time.Millisecond

// Close cancels handlers and closes both normal and CONNECT-hijacked sockets.
// It is safe to call concurrently or more than once.
func (p *Proxy) Close() error {
	p.closeOnce.Do(func() {
		lg().Info("proxy stopping")
		unregisterStateSource(p)
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

// proxyProbePath is the one request a proxy answers for itself. A client uses it to tell a claude-master
// proxy from some other service at the same address; it reveals nothing and changes nothing.
const (
	proxyProbePath   = "/.claude-master/probe"
	proxyProbeHeader = "X-Claude-Master"
)

// tunnelOwner is who opened a tunnel to api.anthropic.com: the client's name, and the project its
// CONNECT named (projectHeader), if any.
type tunnelOwner struct {
	client  string
	project string
}

func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == proxyProbePath {
		w.Header().Set(proxyProbeHeader, "1")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodConnect {
		p.counters.connectRejected.Add(1)
		observeConnection("unknown", "rejected")
		http.Error(w, "proxy expects HTTPS CONNECT", http.StatusBadRequest)
		return
	}
	host, err := proxyAuthority(r.RequestURI)
	if err != nil || r.Host != r.RequestURI || r.ContentLength > 0 || len(r.TransferEncoding) != 0 {
		p.counters.connectRejected.Add(1)
		observeConnection("unknown", "rejected")
		lg().Debug("CONNECT refused: not an acceptable HTTPS authority")
		http.Error(w, "invalid CONNECT authority", http.StatusBadRequest)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		p.counters.connectRejected.Add(1)
		observeConnection("unknown", "rejected")
		http.Error(w, "CONNECT unavailable", http.StatusInternalServerError)
		return
	}
	raw, buffered, err := hijacker.Hijack()
	if err != nil {
		p.counters.connectRejected.Add(1)
		observeConnection("unknown", "rejected")
		return
	}
	client := p.track(raw)
	if client == nil {
		p.counters.connectRejected.Add(1)
		observeConnection("unknown", "rejected")
		return
	}
	p.counters.connectAccepted.Add(1)
	clientName, listenerKind := identifyClient(raw)
	observeConnection(listenerKind, "accepted")
	if _, seen := p.seen.LoadOrStore(clientName+"|"+listenerKind, true); !seen {
		lg().Info("client connected for the first time", "client", clientName, "listener", listenerKind)
	}
	lg().Debug("client tunnel", "client", clientName, "listener", listenerKind, "host", host)
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
		p.clients.Store(tlsConn, tunnelOwner{client: clientName, project: sanitizeProject(r.Header.Get(projectHeader))})
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
	if p.isDraining() {
		// The client's next request opens a new connection, which the balancer (if any) sends to the
		// server that is taking over.
		w.Header().Set("Connection", "close")
	}
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
		// part of the selected inference account's request. The backend preserves
		// native content, while its executor replaces account/device identity and
		// owns the selected subscription's login.
		request, errRequest := http.NewRequestWithContext(r.Context(), http.MethodPost, path, r.Body)
		if errRequest != nil {
			p.counters.blockedRequests.Add(1)
			http.Error(w, "invalid inference request", http.StatusBadRequest)
			return
		}
		request.ContentLength = r.ContentLength
		request.Header = coreexecutor.NativeClaudeProtocolHeaders(r.Header)
		request.Header.Set("Content-Type", "application/json")
		request = request.WithContext(coreexecutor.WithNativeClaudeProtocolHeaders(request.Context(), request.Header))
		if !p.beginInference() {
			p.counters.blockedRequests.Add(1)
			writeProxyRestarting(w)
			return
		}
		defer p.endInference()
		p.counters.inferenceRequests.Add(1)
		p.inference.ServeHTTP(w, request)
		return
	}
	// Everything else goes to Anthropic unchanged, on the session's own login: exactly what Claude Code does
	// without claude-master. claude-master adds no credential to these requests, so relaying them grants
	// nothing; refusing them only broke sessions (WebFetch's domain check, 2.1.296's /api/hello, claude.ai MCP
	// connectors, telemetry). Compatibility comes first. A route not in the reviewed list is logged once per
	// shape, for visibility only.
	if !proxyListedRoute(r, path) {
		p.noteRoute(r.Method, path)
	}
	p.counters.controlRequests.Add(1)
	observeControl(r)
	p.control.ServeHTTP(w, r)
}

// proxyListedRoute: routes reviewed as Claude Code control traffic (proxyControlPath, the legacy Remote
// Control sessions). Only the "unlisted route" log line depends on it; every route is relayed.
func proxyListedRoute(r *http.Request, path string) bool {
	if path == "/v1/sessions" || strings.HasPrefix(path, "/v1/sessions/") {
		return proxyLegacySessionPath(r.Method, path) && proxyLegacySessionRequest(r)
	}
	return proxyControlPath(r.Method, path)
}

// noteRoute logs the first request of each unlisted route shape this process relays: the method and the
// path with id-like segments replaced, never a query or an id. The reference deployment alarms on the
// line (UnlistedRoute) as a review queue: the route already works; list it once it is reviewed.
func (p *Proxy) noteRoute(method, path string) {
	shape := proxyRouteShape(path)
	if _, seen := p.routes.LoadOrStore(method+" "+shape, struct{}{}); seen {
		return
	}
	lg().Info("relayed an unlisted Anthropic route", "method", method, "route", shape)
}

// proxyRouteShape replaces id-like segments (with a digit, or longer than 24 characters) by :id; an API
// version segment (v1, v0) is kept.
func proxyRouteShape(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if proxyVersionSegment(segment) {
			continue
		}
		if len(segment) > 24 || strings.ContainsAny(segment, "0123456789") {
			segments[i] = ":id"
		}
	}
	shape := strings.Join(segments, "/")
	if len(shape) > 120 {
		shape = shape[:120]
	}
	return shape
}

// The legacy namespace also hosts managed agents. Permit only the native BYOC
// Remote Control operations seen in the 2.1.209--2.1.270 audit, never its whole
// subtree or a generic SDK session-creation payload.
func proxyLegacySessionPath(method, path string) bool {
	if path == "/v1/sessions" {
		return method == http.MethodPost
	}
	suffix, ok := strings.CutPrefix(path, "/v1/sessions/")
	if !ok {
		return false
	}
	parts := strings.Split(suffix, "/")
	if !proxyControlID(parts[0]) {
		return false
	}
	if len(parts) == 1 {
		return method == http.MethodGet || method == http.MethodPatch
	}
	if len(parts) != 2 || method != http.MethodPost {
		return false
	}
	return parts[1] == "events" || parts[1] == "archive" || parts[1] == "unarchive"
}

func proxyLegacySessionRequest(r *http.Request) bool {
	byoc := false
	environments := false
	for _, value := range r.Header.Values("Anthropic-Beta") {
		for _, token := range strings.Split(value, ",") {
			token = strings.TrimSpace(token)
			if strings.Contains(strings.ToLower(token), "managed-agents") {
				return false
			}
			if token == "ccr-byoc-2025-07-29" {
				byoc = true
			}
			if token == "environments-2025-11-01" {
				environments = true
			}
		}
	}
	crudProfile := byoc && proxyControlID(r.Header.Get("X-Organization-Uuid"))
	// Permission responses and archive calls from the bridge worker use a
	// different audited header profile, with no organization header. Do not
	// impose the CLI CRUD profile on this native bridge callback path.
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/sessions/"), "/")
	bridgeCallback := r.Method == http.MethodPost && len(parts) == 2 && (parts[1] == "events" || parts[1] == "archive")
	bridgeProfile := environments && bridgeCallback && proxyLegacyRunnerVersion(r.Header.Get("X-Environment-Runner-Version"))
	if !crudProfile && !bridgeProfile {
		return false
	}
	if r.URL.Path != "/v1/sessions" {
		return true
	}
	if r.Body == nil || (r.Header.Get("Content-Encoding") != "" && r.Header.Get("Content-Encoding") != "identity") {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, backendMaxBodyBytes+1))
	_ = r.Body.Close()
	if err != nil || len(raw) > backendMaxBodyBytes {
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil || body == nil {
		return false
	}
	for key := range body {
		switch key {
		case "source", "events", "session_context", "environment_id", "self_hosted_runner_pool_id", "title", "tags", "permission_mode":
		default:
			return false
		}
	}
	var source string
	if json.Unmarshal(body["source"], &source) != nil || source != "remote-control" {
		return false
	}
	var events []json.RawMessage
	if json.Unmarshal(body["events"], &events) != nil || events == nil {
		return false
	}
	var session map[string]json.RawMessage
	if json.Unmarshal(body["session_context"], &session) != nil || session == nil {
		return false
	}
	for key := range session {
		switch key {
		case "sources", "outcomes", "model", "cwd", "reuse_outcome_branches":
		default:
			return false
		}
	}
	_, environment := body["environment_id"]
	_, pool := body["self_hosted_runner_pool_id"]
	if environment == pool {
		return false
	}
	key := "environment_id"
	if pool {
		key = "self_hosted_runner_pool_id"
	}
	var worker string
	return json.Unmarshal(body[key], &worker) == nil && proxyControlID(worker)
}

func proxyLegacyRunnerVersion(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if len(part) == 0 || len(part) > 8 {
			return false
		}
		for _, char := range part {
			if char < '0' || char > '9' {
				return false
			}
		}
	}
	return true
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
	// WebFetch asks whether a domain may be fetched before fetching it; it carries no credential.
	if path == "/api/web/domain_info" {
		return method == http.MethodGet
	}
	// Claude Code checks it can reach Anthropic before an interactive session starts (2.1.296 aborts startup
	// with "Unable to connect to Anthropic services" on a 403 here). It carries no credential.
	if path == "/api/hello" {
		return method == http.MethodGet || method == http.MethodHead
	}
	// The MCP artifacts feature (Claude Code 2.1.296): reviewed when it first appeared; not inference.
	if path == "/api/artifacts/mcp" || path == "/v1/code/mcp/ccr-artifacts" {
		return true
	}
	// Claude Code's feature flags, evaluated remotely under its SDK client key (one segment): reviewed
	// 2026-10-10 from the unlisted-route log, where it reappeared after every server restart. Not inference.
	if key, ok := strings.CutPrefix(path, "/api/eval/"); ok {
		return method == http.MethodPost && proxyControlID(key)
	}
	// Reviewed 2026-10-10 against every endpoint Claude Code 2.1.296 carries: settings, telemetry, onboarding,
	// memory, plugins, frames, transcript sharing, voice, claude.ai MCP connectors, skills, design, the
	// filestore reader and ultrareview's checks. None of them is inference the pool could serve; they belong
	// on the person's own login. Before this the MCP connectors and the rest under /v1 were refused for every
	// pooled session. Still closed: /v1/complete and /v1/messages/batches (inference), /v1/files (uploads
	// would land on the person's account, where pooled inference cannot see them) and
	// /v1/messages/cache_touch (it must reach the conversation's own subscription).
	for _, prefix := range []string{
		"/api/event_logging/", "/api/frame/", "/api/organizations/", "/api/users/",
		"/v1/code/memory/", "/v1/code/local/memory/", "/v1/mcp/", "/v1/toolbox/shttp/mcp/", "/v1/design/",
		"/v1/filestore/fs/", "/v1/ultrareview/",
	} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	switch path {
	case "/api/claude_code/notification/preferences", "/api/claude_code/organizations/metrics_enabled",
		"/api/claude_code/skills", "/api/claude_code_grove", "/api/claude_code_shared_session_transcripts",
		"/api/directory/plugins", "/api/organization/claude_code_first_token_date", "/api/v2/rum",
		"/api/ws/speech_to_text/voice_stream", "/v1/code/mcp/hearthbot",
		"/v1/me", "/v1/organizations/spend_limits", "/v1/skills", "/v1/pages/mcp", "/v1/logs", "/v1/metrics", "/v1/traces":
		return true
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

// The environment routes audited native Claude Code releases use to acquire, acknowledge, renew, stop
// and reconnect bridge work. Being listed only keeps a route out of the unlisted-route log; every route
// is relayed.
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

// metricsState feeds the active-connection gauge.
func (p *Proxy) metricsState() stateSnapshot {
	snap := p.Snapshot()
	return stateSnapshot{ActiveConns: int64(snap.ActiveConnections), HasActiveConns: true}
}

func proxyVersionSegment(segment string) bool {
	if len(segment) < 2 || len(segment) > 3 || segment[0] != 'v' {
		return false
	}
	for _, c := range segment[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
