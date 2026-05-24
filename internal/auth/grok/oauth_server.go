package grok

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// corsAllowedOrigins is the set of origins that may issue preflight requests to
// the loopback callback. Matches opencode's CORS_ALLOWED_ORIGINS allowlist.
var corsAllowedOrigins = map[string]bool{
	"https://accounts.x.ai": true,
	"https://auth.x.ai":     true,
}

// OAuthServer handles the local HTTP callback for the xAI authorization-code flow.
// It binds to 127.0.0.1:56121 — the port registered with xAI for the Grok-CLI
// client; mismatches are rejected at the authorize step.
type OAuthServer struct {
	server     *http.Server
	resultChan chan *OAuthResult
	errorChan  chan error
	mu         sync.Mutex
	running    bool
}

// OAuthResult holds the parameters extracted from the OAuth callback.
type OAuthResult struct {
	// Code is the authorization code from the provider.
	Code string
	// State is the state parameter used to prevent CSRF attacks.
	State string
	// Error is set when the provider reports an OAuth error.
	Error string
}

// NewOAuthServer creates a new OAuthServer. The port is fixed at OAuthCallbackPort
// (56121) — callers must not attempt to change it.
func NewOAuthServer() *OAuthServer {
	return &OAuthServer{
		resultChan: make(chan *OAuthResult, 1),
		errorChan:  make(chan error, 1),
	}
}

// Start binds the server to 127.0.0.1:56121 and begins serving.
// Returns ErrPortInUse when port 56121 is already occupied so the caller can
// fall back to device-code flow without a panic or log.Fatal.
func (s *OAuthServer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("oauth server is already running")
	}

	addr := fmt.Sprintf("%s:%d", OAuthCallbackHost, OAuthCallbackPort)

	// Probe availability before handing off to ListenAndServe so we can return
	// a typed ErrPortInUse rather than a raw syscall error.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return NewAuthenticationError(ErrPortInUse, err)
	}
	// Close the probe listener; ListenAndServe will re-bind.
	_ = ln.Close()

	mux := http.NewServeMux()
	mux.HandleFunc(OAuthCallbackPath, s.handleCallback)

	s.server = &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	s.running = true

	go func() {
		if err := s.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.errorChan <- fmt.Errorf("oauth server error: %w", err)
		}
	}()

	// Brief pause to let the goroutine bind.
	time.Sleep(50 * time.Millisecond)

	log.Debugf("xAI OAuth callback server listening on %s", addr)
	return nil
}

// Stop gracefully shuts down the server.
func (s *OAuthServer) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running || s.server == nil {
		return nil
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	err := s.server.Shutdown(shutdownCtx)
	s.running = false
	s.server = nil
	log.Debug("xAI OAuth callback server stopped")
	return err
}

// WaitForCallback blocks until a callback arrives, an internal error occurs, or
// timeout elapses. Pass 0 for the default 5-minute window.
func (s *OAuthServer) WaitForCallback(timeout time.Duration) (*OAuthResult, error) {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	select {
	case result := <-s.resultChan:
		return result, nil
	case err := <-s.errorChan:
		return nil, err
	case <-time.After(timeout):
		return nil, ErrCallbackTimeout
	}
}

// IsRunning reports whether the server is currently accepting connections.
func (s *OAuthServer) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// handleCallback is the HTTP handler for GET /callback.
func (s *OAuthServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	// Handle CORS preflight from xAI's own auth origins.
	origin := r.Header.Get("Origin")
	if corsAllowedOrigins[origin] {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Private-Network", "true")
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	query := r.URL.Query()
	errorParam := query.Get("error")
	code := query.Get("code")
	state := query.Get("state")

	if errorParam != "" {
		log.Errorf("xAI OAuth error in callback: %s", errorParam)
		s.sendResult(&OAuthResult{Error: errorParam})
		s.serveErrorPage(w, errorParam)
		return
	}

	if code == "" {
		log.Error("xAI OAuth callback missing authorization code")
		s.sendResult(&OAuthResult{Error: "no_code"})
		http.Error(w, "missing authorization code", http.StatusBadRequest)
		return
	}

	if state == "" {
		log.Error("xAI OAuth callback missing state parameter")
		s.sendResult(&OAuthResult{Error: "no_state"})
		http.Error(w, "missing state parameter", http.StatusBadRequest)
		return
	}

	log.Debugf("xAI OAuth callback received (state=%s)", state)
	s.sendResult(&OAuthResult{Code: code, State: state})
	s.serveSuccessPage(w)
}

func (s *OAuthServer) serveSuccessPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(successHTML))
}

func (s *OAuthServer) serveErrorPage(w http.ResponseWriter, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	page := strings.ReplaceAll(errorHTML, "{{ERROR}}", errMsg)
	_, _ = w.Write([]byte(page))
}

func (s *OAuthServer) sendResult(result *OAuthResult) {
	select {
	case s.resultChan <- result:
	default:
		log.Warn("xAI OAuth result channel full, result dropped")
	}
}
