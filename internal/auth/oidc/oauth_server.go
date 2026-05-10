package oidc

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

type OAuthResult struct {
	Code             string
	State            string
	Error            string
	ErrorDescription string
}

type OAuthServer struct {
	server  *http.Server
	port    int
	path    string
	result  chan *OAuthResult
	errChan chan error
	mu      sync.Mutex
	running bool
}

func NewOAuthServer(port int, path string) *OAuthServer {
	return &OAuthServer{
		port:    port,
		path:    normalizeURLPath(path),
		result:  make(chan *OAuthResult, 1),
		errChan: make(chan error, 1),
	}
}

func (s *OAuthServer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return fmt.Errorf("oidc oauth server already running")
	}
	mux := http.NewServeMux()
	mux.HandleFunc(s.path, s.handleCallback)
	s.server = &http.Server{
		Addr:              fmt.Sprintf(":%d", s.port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      10 * time.Second,
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
	result := &OAuthResult{
		Code:             strings.TrimSpace(query.Get("code")),
		State:            strings.TrimSpace(query.Get("state")),
		Error:            strings.TrimSpace(query.Get("error")),
		ErrorDescription: strings.TrimSpace(query.Get("error_description")),
	}
	if result.Code == "" && result.Error == "" {
		result.Error = "missing_code"
	}
	select {
	case s.result <- result:
	default:
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if result.Error != "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("<html><body>OIDC authentication failed. You can close this window.</body></html>"))
		return
	}
	_, _ = w.Write([]byte("<html><body>OIDC authentication complete. You can close this window.</body></html>"))
}
