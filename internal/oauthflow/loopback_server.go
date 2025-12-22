package oauthflow

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ErrCallbackTimeout is returned when the local loopback server does not receive a callback in time.
var ErrCallbackTimeout = errors.New("oauthflow: callback timeout")

// CallbackResult contains query parameters returned to the redirect URI.
type CallbackResult struct {
	Code  string
	State string
	Error string
}

// LoopbackServer is a loopback-only HTTP server for OAuth native-app callbacks.
// It binds to 127.0.0.1 and captures a single callback result.
type LoopbackServer struct {
	server       *http.Server
	listener     net.Listener
	port         int
	callbackPath string

	resultChan chan CallbackResult
	errorChan  chan error

	mu      sync.Mutex
	running bool
}

// NewLoopbackServer creates a new loopback callback server.
// callbackPath must be an absolute path (e.g., "/callback").
func NewLoopbackServer(port int, callbackPath string) *LoopbackServer {
	path := strings.TrimSpace(callbackPath)
	if path == "" {
		path = "/callback"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return &LoopbackServer{
		port:         port,
		callbackPath: path,
		resultChan:   make(chan CallbackResult, 1),
		errorChan:    make(chan error, 1),
	}
}

// CallbackPath returns the server callback path.
func (s *LoopbackServer) CallbackPath() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.callbackPath
}

// Port returns the actual bound port once the server has started.
func (s *LoopbackServer) Port() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.port
}

// Start binds and serves the callback endpoint.
func (s *LoopbackServer) Start() error {
	if s == nil {
		return fmt.Errorf("oauthflow: server is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return fmt.Errorf("oauthflow: server already running")
	}

	mux := http.NewServeMux()
	mux.HandleFunc(s.callbackPath, s.handleCallback)

	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return err
		}
		return err
	}

	s.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
	}
	s.listener = ln
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		s.port = tcp.Port
	}
	s.running = true

	go func() {
		if err := s.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case s.errorChan <- err:
			default:
			}
		}
	}()

	return nil
}

// Stop gracefully shuts down the callback server.
func (s *LoopbackServer) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.server == nil {
		return nil
	}

	if ctx == nil {
		ctx = context.Background()
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := s.server.Shutdown(shutdownCtx)
	if s.listener != nil {
		_ = s.listener.Close()
		s.listener = nil
	}
	s.running = false
	s.server = nil
	return err
}

// WaitForCallback blocks until a callback result, server error, timeout, or context cancellation.
func (s *LoopbackServer) WaitForCallback(ctx context.Context, timeout time.Duration) (CallbackResult, error) {
	if s == nil {
		return CallbackResult{}, fmt.Errorf("oauthflow: server is nil")
	}
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	if ctx == nil {
		ctx = context.Background()
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case res := <-s.resultChan:
		return res, nil
	case err := <-s.errorChan:
		return CallbackResult{}, err
	case <-ctx.Done():
		return CallbackResult{}, ctx.Err()
	case <-timer.C:
		return CallbackResult{}, ErrCallbackTimeout
	}
}

func (s *LoopbackServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	query := r.URL.Query()
	res := CallbackResult{
		Code:  strings.TrimSpace(query.Get("code")),
		State: strings.TrimSpace(query.Get("state")),
		Error: strings.TrimSpace(query.Get("error")),
	}

	select {
	case s.resultChan <- res:
	default:
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if res.Error != "" || res.Code == "" {
		_, _ = w.Write([]byte("<h1>Login failed</h1><p>Please check the CLI output.</p><p>You can close this window.</p>"))
		return
	}
	_, _ = w.Write([]byte("<h1>Login successful</h1><p>You can close this window.</p>"))
}
