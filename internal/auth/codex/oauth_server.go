package codex

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

// OAuthServer handles the local HTTP server for OAuth callbacks.
// It listens for the authorization code response from the OAuth provider
// and captures the necessary parameters to complete the authentication flow.
type OAuthServer struct {
	// server is the underlying HTTP server instance
	server *http.Server
	// port is the port number on which the server listens
	port int
	// resultChan is a channel for sending OAuth results
	resultChan chan *OAuthResult
	// errorChan is a channel for sending OAuth errors
	errorChan chan error
	// mu is a mutex for protecting server state
	mu sync.Mutex
	// running indicates whether the server is currently running
	running bool
}

// OAuthResult contains the result of the OAuth callback.
// It holds either the authorization code and state for successful authentication
// or an error message if the authentication failed.
type OAuthResult struct {
	// Code is the authorization code received from the OAuth provider
	Code string
	// State is the state parameter used to prevent CSRF attacks
	State string
	// Error contains any error message if the OAuth flow failed
	Error string
}

// NewOAuthServer creates a new OAuth callback server.
// It initializes the server with the specified port and creates channels
// for handling OAuth results and errors.
//
// Parameters:
//   - port: The port number on which the server should listen
//
// Returns:
//   - *OAuthServer: A new OAuthServer instance
func NewOAuthServer(port int) *OAuthServer {
	return &OAuthServer{
		port:       port,
		resultChan: make(chan *OAuthResult, 1),
		errorChan:  make(chan error, 1),
	}
}

// Start starts the OAuth callback server.
// It sets up the HTTP handlers for the callback and success endpoints,
// and begins listening on the specified port.
//
// Returns:
//   - error: An error if the server fails to start
func (s *OAuthServer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("server is already running")
	}

	// Check if port is available
	if !s.isPortAvailable() {
		return fmt.Errorf("port %d is already in use", s.port)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", s.handleCallback)
	mux.HandleFunc("/success", s.handleSuccess)

	s.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", s.port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	s.running = true

	// Start server in goroutine
	go func() {
		if err := s.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.errorChan <- fmt.Errorf("server failed to start: %w", err)
		}
	}()

	// Give server a moment to start
	time.Sleep(100 * time.Millisecond)

	return nil
}

// Stop gracefully stops the OAuth callback server.
// It performs a graceful shutdown of the HTTP server with a timeout.
//
// Parameters:
//   - ctx: The context for controlling the shutdown process
//
// Returns:
//   - error: An error if the server fails to stop gracefully
func (s *OAuthServer) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running || s.server == nil {
		return nil
	}

	log.Debug("Stopping OAuth callback server")

	// Create a context with timeout for shutdown
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	err := s.server.Shutdown(shutdownCtx)
	s.running = false
	s.server = nil

	return err
}

// WaitForCallback waits for the OAuth callback with a timeout.
// It blocks until either an OAuth result is received, an error occurs,
// or the specified timeout is reached.
//
// Parameters:
//   - timeout: The maximum time to wait for the callback
//
// Returns:
//   - *OAuthResult: The OAuth result if successful
//   - error: An error if the callback times out or an error occurs
func (s *OAuthServer) WaitForCallback(timeout time.Duration) (*OAuthResult, error) {
	select {
	case result := <-s.resultChan:
		return result, nil
	case err := <-s.errorChan:
		return nil, err
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout waiting for OAuth callback")
	}
}

// handleCallback handles the OAuth callback endpoint.
// It extracts the authorization code and state from the callback URL,
// validates the parameters, and sends the result to the waiting channel.
//
// Parameters:
//   - w: The HTTP response writer
//   - r: The HTTP request
func (s *OAuthServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	log.Debug("Received OAuth callback")

	// Validate request method
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract parameters
	query := r.URL.Query()
	code := query.Get("code")
	state := query.Get("state")
	errorParam := query.Get("error")

	// Validate required parameters
	if errorParam != "" {
		log.Errorf("OAuth error received: %s", errorParam)
		result := &OAuthResult{
			Error: errorParam,
		}
		s.sendResult(result)
		http.Error(w, fmt.Sprintf("OAuth error: %s", errorParam), http.StatusBadRequest)
		return
	}

	if code == "" {
		log.Error("No authorization code received")
		result := &OAuthResult{
			Error: "no_code",
		}
		s.sendResult(result)
		http.Error(w, "No authorization code received", http.StatusBadRequest)
		return
	}

	if state == "" {
		log.Error("No state parameter received")
		result := &OAuthResult{
			Error: "no_state",
		}
		s.sendResult(result)
		http.Error(w, "No state parameter received", http.StatusBadRequest)
		return
	}

	// Send successful result
	result := &OAuthResult{
		Code:  code,
		State: state,
	}
	s.sendResult(result)

	// Redirect to success page
	http.Redirect(w, r, "/success", http.StatusFound)
}

// handleSuccess handles the success page endpoint.
// It serves a user-friendly HTML page indicating that authentication was successful.
//
// Parameters:
//   - w: The HTTP response writer
//   - r: The HTTP request
func (s *OAuthServer) handleSuccess(w http.ResponseWriter, r *http.Request) {
	log.Debug("Serving success page")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	// Parse query parameters for customization
	query := r.URL.Query()
	setupRequired := query.Get("setup_required") == "true"
	platformURL := query.Get("platform_url")
	if platformURL == "" {
		platformURL = "https://platform.openai.com"
	}

	// Generate success page HTML with dynamic content
	successHTML := s.generateSuccessHTML(setupRequired, platformURL)

	_, err := w.Write([]byte(successHTML))
	if err != nil {
		log.Errorf("Failed to write success page: %v", err)
	}
}

// generateSuccessHTML creates the HTML content for the success page.
// It customizes the page based on whether additional setup is required
// and includes a link to the platform.
//
// Parameters:
//   - setupRequired: Whether additional setup is required after authentication
//   - platformURL: The URL to the platform for additional setup
//
// Returns:
//   - string: The HTML content for the success page
func (s *OAuthServer) generateSuccessHTML(setupRequired bool, platformURL string) string {
	html := LoginSuccessHtml

	// Replace platform URL placeholder
	html = strings.Replace(html, "{{PLATFORM_URL}}", platformURL, -1)

	// Add setup notice if required
	if setupRequired {
		setupNotice := strings.Replace(SetupNoticeHtml, "{{PLATFORM_URL}}", platformURL, -1)
		html = strings.Replace(html, "{{SETUP_NOTICE}}", setupNotice, 1)
	} else {
		html = strings.Replace(html, "{{SETUP_NOTICE}}", "", 1)
	}

	return html
}

// sendResult sends the OAuth result to the waiting channel.
// It ensures that the result is sent without blocking the handler.
//
// Parameters:
//   - result: The OAuth result to send
func (s *OAuthServer) sendResult(result *OAuthResult) {
	select {
	case s.resultChan <- result:
		log.Debug("OAuth result sent to channel")
	default:
		log.Warn("OAuth result channel is full, result dropped")
	}
}

// isPortAvailable checks if the specified port is available.
// It attempts to listen on the port to determine availability.
//
// Returns:
//   - bool: True if the port is available, false otherwise
func (s *OAuthServer) isPortAvailable() bool {
	addr := fmt.Sprintf(":%d", s.port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	defer func() {
		_ = listener.Close()
	}()
	return true
}

// IsRunning returns whether the server is currently running.
//
// Returns:
//   - bool: True if the server is running, false otherwise
func (s *OAuthServer) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}
