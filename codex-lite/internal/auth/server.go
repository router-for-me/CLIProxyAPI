package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type OAuthServer struct {
	server     *http.Server
	port       int
	resultChan chan *OAuthResult
	mu         sync.Mutex
	running    bool
}

type OAuthResult struct {
	Code  string
	State string
	Error string
}

func NewOAuthServer(port int) *OAuthServer {
	return &OAuthServer{
		port:       port,
		resultChan: make(chan *OAuthResult, 1),
	}
}

func (s *OAuthServer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("server already running")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", s.handleCallback)
	mux.HandleFunc("/success", s.handleSuccess)

	s.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: mux,
	}

	s.running = true
	go s.server.ListenAndServe()
	time.Sleep(100 * time.Millisecond)
	return nil
}

func (s *OAuthServer) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running || s.server == nil {
		return nil
	}

	err := s.server.Shutdown(ctx)
	s.running = false
	return err
}

func (s *OAuthServer) WaitForCallback(timeout time.Duration) (*OAuthResult, error) {
	select {
	case result := <-s.resultChan:
		return result, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout waiting for callback")
	}
}

func (s *OAuthServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	code := query.Get("code")
	state := query.Get("state")
	errParam := query.Get("error")

	result := &OAuthResult{
		Code:  code,
		State: state,
		Error: errParam,
	}

	select {
	case s.resultChan <- result:
	default:
	}

	if errParam != "" {
		http.Error(w, errParam, http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/success", http.StatusFound)
}

func (s *OAuthServer) handleSuccess(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html><head><title>Login Success</title></head>
<body style="font-family:sans-serif;text-align:center;padding:50px">
<h1>Login Successful</h1>
<p>You can close this window.</p>
</body></html>`))
}
