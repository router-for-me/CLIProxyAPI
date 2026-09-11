package devin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	// DefaultAppBaseURL is the user-facing web app for Devin OAuth.
	DefaultAppBaseURL = "https://app.devin.ai"
	// DefaultAPIBaseURL is the API backend for token exchange and user status.
	DefaultAPIBaseURL = "https://api.devin.ai"
	// DefaultServerURL is the upstream Codeium/Devin reasoning backend.
	DefaultServerURL = "https://server.codeium.com"

	devinTokenPrefix = "devin-session-token$"
)

// DevinTokenBundle holds the completed authentication result.
type DevinTokenBundle struct {
	SessionToken string `json:"session_token"`
	RawToken     string `json:"raw_token"`
	UserName     string `json:"user_name,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	OrgID        string `json:"org_id,omitempty"`
	BaseURL      string `json:"base_url,omitempty"`
}

// DevinAuthService coordinates Devin PKCE authorization and token exchange.
type DevinAuthService struct {
	client        *http.Client
	appBaseURL    string
	apiBaseURL    string
	serverBaseURL string
}

// NewDevinAuthService creates a new Devin authentication service instance.
func NewDevinAuthService(client *http.Client) *DevinAuthService {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &DevinAuthService{
		client:        client,
		appBaseURL:    DefaultAppBaseURL,
		apiBaseURL:    DefaultAPIBaseURL,
		serverBaseURL: DefaultServerURL,
	}
}

// SetServerBaseURL overrides the upstream reasoning/seat management base URL (used in tests).
func (s *DevinAuthService) SetServerBaseURL(url string) {
	if s != nil && strings.TrimSpace(url) != "" {
		s.serverBaseURL = strings.TrimRight(strings.TrimSpace(url), "/")
	}
}

// BuildAuthorizationURL constructs the PKCE login URL.
func (s *DevinAuthService) BuildAuthorizationURL(redirectURI, codeChallenge, state string) string {
	q := url.Values{}
	q.Set("redirect_uri", redirectURI)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	q.Set("prompt", "select_account")
	if state != "" {
		q.Set("state", state)
	}
	return fmt.Sprintf("%s/auth/cli/continue?%s", strings.TrimRight(s.appBaseURL, "/"), q.Encode())
}

// ExchangeCodeForToken exchanges the authorization code for a session token.
func (s *DevinAuthService) ExchangeCodeForToken(ctx context.Context, code, codeVerifier string) (string, error) {
	reqBody := map[string]string{
		"code":          strings.TrimSpace(code),
		"code_verifier": strings.TrimSpace(codeVerifier),
	}
	jsonBody, errMarshal := json.Marshal(reqBody)
	if errMarshal != nil {
		return "", errMarshal
	}

	endpoint := fmt.Sprintf("%s/auth/cli/token", strings.TrimRight(s.apiBaseURL, "/"))
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(jsonBody))
	if errReq != nil {
		return "", errReq
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, errDo := s.client.Do(req)
	if errDo != nil {
		return "", fmt.Errorf("devin token exchange failed: %w", errDo)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	respBytes, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return "", fmt.Errorf("read token exchange response: %w", errRead)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("token exchange failed with status %d: %s", resp.StatusCode, string(respBytes))
	}

	token := strings.TrimSpace(gjson.GetBytes(respBytes, "token").String())
	if token == "" {
		return "", fmt.Errorf("response did not contain a valid token: %s", string(respBytes))
	}

	return token, nil
}

// FetchSelfProfile retrieves the authenticated user's profile from api.devin.ai/v3/self.
func (s *DevinAuthService) FetchSelfProfile(ctx context.Context, sessionToken string) (userName, userID, orgID string, err error) {
	endpoint := fmt.Sprintf("%s/v3/self", strings.TrimRight(s.apiBaseURL, "/"))
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if errReq != nil {
		return "", "", "", errReq
	}
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	req.Header.Set("Accept", "application/json")

	resp, errDo := s.client.Do(req)
	if errDo != nil {
		return "", "", "", errDo
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	respBytes, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return "", "", "", errRead
	}

	if resp.StatusCode == http.StatusOK {
		root := gjson.ParseBytes(respBytes)
		userName = root.Get("user_name").String()
		userID = root.Get("user_id").String()
		orgID = root.Get("org_id").String()
	}

	return userName, userID, orgID, nil
}

// FormatSessionToken ensures the token carries the mandatory devin-session-token$ prefix.
func FormatSessionToken(rawToken string) string {
	t := strings.TrimSpace(rawToken)
	if strings.HasPrefix(t, devinTokenPrefix) {
		return t
	}
	if strings.HasPrefix(t, "eyJ") {
		return devinTokenPrefix + t
	}
	return t
}

// OAuthServer handles local loopback HTTP callbacks for Devin authentication.
type OAuthServer struct {
	server     *http.Server
	listener   net.Listener
	port       int
	resultChan chan *OAuthResult
	errorChan  chan error
	mu         sync.Mutex
	running    bool
}

// OAuthResult carries the authorization code from the browser callback.
type OAuthResult struct {
	Code  string
	State string
	Error string
}

// NewOAuthServer creates a local loopback server for Devin OAuth.
func NewOAuthServer(port int) *OAuthServer {
	return &OAuthServer{
		port:       port,
		resultChan: make(chan *OAuthResult, 1),
		errorChan:  make(chan error, 1),
	}
}

// Start initiates the local HTTP server. If port is 0, an available ephemeral port is chosen.
func (s *OAuthServer) Start() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return s.port, nil
	}

	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	ln, errLn := net.Listen("tcp", addr)
	if errLn != nil {
		return 0, fmt.Errorf("failed to bind local OAuth server to %s: %w", addr, errLn)
	}

	s.listener = ln
	s.port = ln.Addr().(*net.TCPAddr).Port

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", s.handleCallback)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/callback" {
			s.handleCallback(w, r)
			return
		}
		http.NotFound(w, r)
	})

	s.server = &http.Server{
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	s.running = true
	go func() {
		if errServe := s.server.Serve(ln); errServe != nil && !errors.Is(errServe, http.ErrServerClosed) {
			log.Debugf("devin oauth server error: %v", errServe)
			select {
			case s.errorChan <- errServe:
			default:
			}
		}
	}()

	return s.port, nil
}

// Stop gracefully stops the server.
func (s *OAuthServer) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}
	s.running = false
	if s.server != nil {
		return s.server.Shutdown(ctx)
	}
	return nil
}

// WaitForCallback waits for the browser redirect or times out.
func (s *OAuthServer) WaitForCallback(timeout time.Duration) (*OAuthResult, error) {
	select {
	case res := <-s.resultChan:
		return res, nil
	case err := <-s.errorChan:
		return nil, err
	case <-time.After(timeout):
		return nil, errors.New("devin authentication timed out")
	}
}

func (s *OAuthServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	code := strings.TrimSpace(q.Get("code"))
	state := strings.TrimSpace(q.Get("state"))
	errStr := strings.TrimSpace(q.Get("error"))
	errDesc := strings.TrimSpace(q.Get("error_description"))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if errStr != "" || code == "" {
		errMsg := errStr
		if errDesc != "" {
			errMsg = fmt.Sprintf("%s: %s", errStr, errDesc)
		}
		if errMsg == "" {
			errMsg = "missing authorization code"
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(fmt.Sprintf(loginFailureHTML, errMsg)))
		select {
		case s.resultChan <- &OAuthResult{Error: errMsg}:
		default:
		}
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(loginSuccessHTML))

	select {
	case s.resultChan <- &OAuthResult{Code: code, State: state}:
	default:
	}
}

const loginSuccessHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <title>Authentication Successful - Devin</title>
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; display: flex; justify-content: center; align-items: center; height: 100vh; margin: 0; background: #0f172a; color: #f8fafc; }
        .card { background: #1e293b; padding: 2.5rem; border-radius: 12px; box-shadow: 0 8px 30px rgba(0,0,0,0.4); text-align: center; max-width: 420px; }
        h2 { margin-top: 0; color: #38bdf8; }
        p { color: #94a3b8; font-size: 15px; }
    </style>
</head>
<body>
    <div class="card">
        <h2>Authentication Complete</h2>
        <p>You have successfully logged in to Devin via CLIProxyAPI.</p>
        <p>You may safely close this window and return to your terminal.</p>
    </div>
</body>
</html>`

const loginFailureHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <title>Authentication Failed - Devin</title>
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; display: flex; justify-content: center; align-items: center; height: 100vh; margin: 0; background: #0f172a; color: #f8fafc; }
        .card { background: #1e293b; padding: 2.5rem; border-radius: 12px; box-shadow: 0 8px 30px rgba(0,0,0,0.4); text-align: center; max-width: 420px; }
        h2 { margin-top: 0; color: #f87171; }
        p { color: #94a3b8; font-size: 15px; }
    </style>
</head>
<body>
    <div class="card">
        <h2>Authentication Failed</h2>
        <p>Devin authentication encountered an error: %s</p>
        <p>Please check your terminal and try again.</p>
    </div>
</body>
</html>`
