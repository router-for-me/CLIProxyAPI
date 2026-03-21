package localrouting

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type EdgeProxy struct {
	store    *RouteStore
	edgePort int
	https    bool
	certs    *CertManager

	mu       sync.Mutex
	server   *http.Server
	listener net.Listener
	owner    bool
}

func NewEdgeProxy(store *RouteStore, edgePort int, https bool) *EdgeProxy {
	if edgePort <= 0 {
		edgePort = DefaultEdgePort
	}
	stateDir := ""
	if store != nil {
		stateDir = store.StateDir()
	}
	return &EdgeProxy{
		store:    store,
		edgePort: edgePort,
		https:    https,
		certs:    NewCertManager(stateDir, store),
	}
}

func (p *EdgeProxy) StartOrReuse() (bool, error) {
	if p == nil {
		return false, fmt.Errorf("edge proxy is nil")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.server != nil {
		return p.owner, nil
	}
	ln, errListen := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p.edgePort))
	if errListen != nil {
		if strings.Contains(strings.ToLower(errListen.Error()), "address already in use") {
			p.owner = false
			return false, nil
		}
		return false, errListen
	}
	handler := &edgeHandler{store: p.store, edgePort: p.edgePort}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if p.https {
		server.TLSConfig = p.certs.TLSConfig()
	}
	p.server = server
	p.listener = ln
	p.owner = true
	_ = os.WriteFile(filepathJoin(p.store.StateDir(), "edge.pid"), []byte(strconv.Itoa(os.Getpid())), 0o644)
	go func() {
		if p.https {
			tlsListener := tls.NewListener(ln, server.TLSConfig)
			_ = server.Serve(tlsListener)
			return
		}
		_ = server.Serve(ln)
	}()
	return true, nil
}

func (p *EdgeProxy) Stop(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	server := p.server
	owner := p.owner
	p.server = nil
	p.listener = nil
	p.owner = false
	p.mu.Unlock()
	if server == nil || !owner {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_ = os.Remove(filepathJoin(p.store.StateDir(), "edge.pid"))
	return server.Shutdown(ctx)
}

type edgeHandler struct {
	store    *RouteStore
	edgePort int
}

func (h *edgeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.store == nil {
		http.Error(w, "local routing is unavailable", http.StatusServiceUnavailable)
		return
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-CLIProxyAPI-Local-Routing-Hop")), "1") {
		w.WriteHeader(http.StatusLoopDetected)
		_, _ = w.Write([]byte("loop detected: your proxy target likely preserved the original host header; enable host rewrite/changeOrigin"))
		return
	}
	host := normalizeRequestHost(r.Host)
	routes, errList := h.store.List()
	if errList != nil {
		http.Error(w, "failed to load local routes", http.StatusBadGateway)
		return
	}
	route, ok := findRoute(host, routes)
	if !ok {
		http.Error(w, "unknown local route", http.StatusNotFound)
		return
	}
	if route.TargetPort == h.edgePort && strings.EqualFold(route.TargetHost, "127.0.0.1") {
		w.WriteHeader(http.StatusLoopDetected)
		_, _ = w.Write([]byte("loop detected: route points back to edge proxy"))
		return
	}
	targetURL := &url.URL{Scheme: "http", Host: net.JoinHostPort(route.TargetHost, strconv.Itoa(route.TargetPort))}
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = route.TargetHost + ":" + strconv.Itoa(route.TargetPort)
		req.Header.Set("X-CLIProxyAPI-Local-Routing-Hop", "1")
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "route backend unavailable", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

func normalizeRequestHost(raw string) string {
	host := strings.ToLower(strings.TrimSpace(raw))
	if host == "" {
		return ""
	}
	if strings.Contains(host, ":") {
		if parsedHost, _, errSplit := net.SplitHostPort(host); errSplit == nil {
			host = parsedHost
		} else {
			host = strings.Split(host, ":")[0]
		}
	}
	return strings.Trim(host, ".")
}

func findRoute(host string, routes []RouteInfo) (RouteInfo, bool) {
	for i := range routes {
		if strings.EqualFold(host, routes[i].Host) {
			return routes[i], true
		}
	}
	for i := range routes {
		h := strings.ToLower(strings.TrimSpace(routes[i].Host))
		if h == "" {
			continue
		}
		if strings.HasSuffix(host, "."+h) {
			return routes[i], true
		}
	}
	return RouteInfo{}, false
}

func filepathJoin(base, name string) string {
	if strings.TrimSpace(base) == "" {
		return name
	}
	return base + string(os.PathSeparator) + name
}
