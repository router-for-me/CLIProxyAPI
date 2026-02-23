package iflow

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const errorRedirectURL = "https://iflow.cn/oauth/error"

// OAuthResult captures the outcome of the local OAuth callback.
type OAuthResult struct {
	Code  string
	State string
	Error string
}

// OAuthServer provides a minimal HTTP server for handling the iFlow OAuth callback.
type OAuthServer struct {
	server  *http.Server
	port    int
	result  chan *OAuthResult
	errChan chan error
	mu      sync.Mutex
	running bool
}

// NewOAuthServer constructs a new OAuthServer bound to the provided port.
func NewOAuthServer(port int) *OAuthServer {
	return &OAuthServer{
		port:    port,
		result:  make(chan *OAuthResult, 1),
		errChan: make(chan error, 1),
	}
}

// Start launches the callback listener.
func (s *OAuthServer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return fmt.Errorf("iflow oauth server already running")
	}
	if !s.isPortAvailable() {
		return fmt.Errorf("port %d is already in use", s.port)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth2callback", s.handleCallback)

	s.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", s.port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	s.running = true

	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.errChan <- err
		}
	}()

	time.Sleep(100 * time.Millisecond)
	return nil
}

// Stop gracefully terminates the callback listener.
func (s *OAuthServer) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.server == nil {
		return nil
	}
	defer func() {
		s.running = false
		s.server = nil
	}()
	return s.server.Shutdown(ctx)
}

// WaitForCallback blocks until a callback result, server error, or timeout occurs.
func (s *OAuthServer) WaitForCallback(timeout time.Duration) (*OAuthResult, error) {
	select {
	case res := <-s.result:
		return res, nil
	case err := <-s.errChan:
		return nil, err
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout waiting for OAuth callback")
	}
}

func (s *OAuthServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	query := r.URL.Query()
	if errParam := strings.TrimSpace(query.Get("error")); errParam != "" {
		s.sendResult(&OAuthResult{Error: errParam})
		http.Redirect(w, r, errorRedirectURL, http.StatusFound)
		return
	}

	code := strings.TrimSpace(query.Get("code"))
	if code == "" {
		s.sendResult(&OAuthResult{Error: "missing_code"})
		http.Redirect(w, r, errorRedirectURL, http.StatusFound)
		return
	}

	state := query.Get("state")
	s.sendResult(&OAuthResult{Code: code, State: state})
	http.Redirect(w, r, SuccessRedirectURL, http.StatusFound)
}

func (s *OAuthServer) sendResult(res *OAuthResult) {
	select {
	case s.result <- res:
	default:
		log.Debug("iflow oauth result channel full, dropping result")
	}
}

func (s *OAuthServer) isPortAvailable() bool {
	addr := fmt.Sprintf(":%d", s.port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}
