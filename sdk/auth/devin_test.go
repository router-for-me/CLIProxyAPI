package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	devinauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/devin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestDevinAuthenticatorProviderAndRefreshLead(t *testing.T) {
	authenticator := NewDevinAuthenticator()
	if authenticator.Provider() != "devin" {
		t.Fatalf("Provider() = %q, want devin", authenticator.Provider())
	}
	lead := authenticator.RefreshLead()
	if lead != nil {
		t.Fatalf("RefreshLead() = %v, want nil for permanent tokens", lead)
	}
}

func TestDevinAuthenticatorHeadlessManualTokenLogin(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/self":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"user_name":"token-user","user_id":"uid-token","org_id":"org-token"}`))
		case devinauth.DevinGetUserStatusPath:
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockServer.Close()

	authSvc := devinauth.NewDevinAuthService(mockServer.Client())
	authSvc.SetAPIBaseURL(mockServer.URL)
	authSvc.SetServerBaseURL(mockServer.URL)

	authenticator := NewDevinAuthenticator()
	authenticator.AuthService = authSvc
	cfg := &config.Config{}

	mockPrompt := func(prompt string) (string, error) {
		return "devin-session-token$eyJmock.session.token", nil
	}

	opts := &LoginOptions{
		NoBrowser: true,
		Prompt:    mockPrompt,
	}

	auth, err := authenticator.Login(context.Background(), cfg, opts)
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}

	if auth.Provider != "devin" {
		t.Errorf("auth.Provider = %q, want devin", auth.Provider)
	}
	if auth.Attributes["api_key"] != "devin-session-token$eyJmock.session.token" {
		t.Errorf("api_key = %q, want expected", auth.Attributes["api_key"])
	}
	if auth.Label != "Devin (token-user)" {
		t.Errorf("auth.Label = %q, want 'Devin (token-user)'", auth.Label)
	}
}

func TestDevinAuthenticatorHeadlessPromptNil(t *testing.T) {
	authenticator := NewDevinAuthenticator()
	cfg := &config.Config{}

	opts := &LoginOptions{
		NoBrowser: true,
		Prompt:    nil,
	}

	_, err := authenticator.Login(context.Background(), cfg, opts)
	if err == nil {
		t.Fatal("expected error when prompt is nil, got nil")
	}
	if !strings.Contains(err.Error(), "requires an interactive prompt") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestDevinAuthenticatorHeadlessManualCodeLogin(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/cli/token":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if body["code"] != "devin-cli-auth-code-123" {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"invalid_code"}`))
				return
			}
			if body["code_verifier"] == "" {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"missing_verifier"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"token":"eyJtest.manual.code.token"}`))

		case "/v3/self":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"user_name":"test-user","user_id":"uid-123","org_id":"org-456"}`))

		case devinauth.DevinGetUserStatusPath:
			w.WriteHeader(http.StatusServiceUnavailable)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockServer.Close()

	authSvc := devinauth.NewDevinAuthService(mockServer.Client())
	authSvc.SetAPIBaseURL(mockServer.URL)
	authSvc.SetServerBaseURL(mockServer.URL)

	authenticator := NewDevinAuthenticator()
	authenticator.AuthService = authSvc
	cfg := &config.Config{}

	mockPrompt := func(prompt string) (string, error) {
		return "devin-cli-auth-code-123", nil
	}

	opts := &LoginOptions{
		NoBrowser: true,
		Prompt:    mockPrompt,
	}

	auth, err := authenticator.Login(context.Background(), cfg, opts)
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}

	if auth.Provider != "devin" {
		t.Errorf("auth.Provider = %q, want devin", auth.Provider)
	}
	expectedToken := "devin-session-token$eyJtest.manual.code.token"
	if auth.Attributes["api_key"] != expectedToken {
		t.Errorf("api_key = %q, want %q", auth.Attributes["api_key"], expectedToken)
	}
	expectedLabel := "Devin (test-user)"
	if auth.Label != expectedLabel {
		t.Errorf("auth.Label = %q, want %q", auth.Label, expectedLabel)
	}
}

func TestDevinAuthenticatorHeadlessManualCallbackURLLogin(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/cli/token":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if body["code"] != "parsed-callback-code" {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"invalid_code"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"token":"eyJtest.callback.token"}`))

		case "/v3/self":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"user_name":"url-user","user_id":"uid-url","org_id":"org-url"}`))

		case devinauth.DevinGetUserStatusPath:
			w.WriteHeader(http.StatusServiceUnavailable)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockServer.Close()

	authSvc := devinauth.NewDevinAuthService(mockServer.Client())
	authSvc.SetAPIBaseURL(mockServer.URL)
	authSvc.SetServerBaseURL(mockServer.URL)

	authenticator := NewDevinAuthenticator()
	authenticator.AuthService = authSvc
	cfg := &config.Config{}

	mockPrompt := func(prompt string) (string, error) {
		// User accidentally pastes the full callback URL from browser address bar
		return "http://127.0.0.1:12345/callback?code=parsed-callback-code", nil
	}

	opts := &LoginOptions{
		NoBrowser: true,
		Prompt:    mockPrompt,
	}

	auth, err := authenticator.Login(context.Background(), cfg, opts)
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}

	expectedToken := "devin-session-token$eyJtest.callback.token"
	if auth.Attributes["api_key"] != expectedToken {
		t.Errorf("api_key = %q, want %q", auth.Attributes["api_key"], expectedToken)
	}
	expectedLabel := "Devin (url-user)"
	if auth.Label != expectedLabel {
		t.Errorf("auth.Label = %q, want %q", auth.Label, expectedLabel)
	}
}

func TestDevinAuthenticatorHeadlessEmptyInputAborts(t *testing.T) {
	authenticator := NewDevinAuthenticator()
	cfg := &config.Config{}

	mockPrompt := func(prompt string) (string, error) {
		return "", nil
	}

	opts := &LoginOptions{
		NoBrowser: true,
		Prompt:    mockPrompt,
	}

	_, err := authenticator.Login(context.Background(), cfg, opts)
	if err == nil {
		t.Fatal("expected error on empty input, got nil")
	}
	if !strings.Contains(err.Error(), "canceled") {
		t.Errorf("expected cancellation error message, got: %v", err)
	}
}

func TestParseDevinManualPaste(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		expectedState string
		wantCode      string
		wantToken     string
		wantErr       string
		wantSentinel  error
	}{
		{name: "empty", input: "  "},
		{name: "quoted empty", input: `"   "`},
		{name: "session token", input: "devin-session-token$eyJabc", wantToken: "devin-session-token$eyJabc"},
		{name: "jwt", input: "eyJabc.def", wantToken: "eyJabc.def"},
		{name: "quoted jwt", input: `"eyJabc"`, wantToken: "eyJabc"},
		{name: "raw code", input: "devin-cli-auth-code-123", wantCode: "devin-cli-auth-code-123"},
		{name: "quoted code", input: `"quoted-code-xyz"`, wantCode: "quoted-code-xyz"},
		{name: "callback without state", input: "http://127.0.0.1:9/callback?code=c1", wantCode: "c1"},
		{name: "callback matching state", input: "http://127.0.0.1:9/callback?code=c1&state=abc", expectedState: "abc", wantCode: "c1"},
		{name: "callback mismatched state", input: "http://127.0.0.1:9/callback?code=stolen-code&state=other", expectedState: "abc", wantErr: "state mismatch"},
		{name: "oauth error", input: "http://127.0.0.1:9/callback?error=access_denied&state=abc", wantErr: "oauth error"},
		{name: "oauth error with description", input: "http://127.0.0.1:9/callback?error=access_denied&error_description=user%20denied", wantErr: "user denied"},
		{name: "unrecognized", input: "not a code, has spaces", wantErr: "unrecognized", wantSentinel: errDevinUnrecognizedPaste},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, token, errPaste := parseDevinManualPaste(tt.input, tt.expectedState)
			if tt.wantErr != "" {
				if errPaste == nil || !strings.Contains(errPaste.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want substring %q", errPaste, tt.wantErr)
				}
				if tt.wantSentinel != nil && !errors.Is(errPaste, tt.wantSentinel) {
					t.Fatalf("error %v is not %v", errPaste, tt.wantSentinel)
				}
				return
			}
			if errPaste != nil {
				t.Fatalf("unexpected error: %v", errPaste)
			}
			if code != tt.wantCode || token != tt.wantToken {
				t.Fatalf("code=%q token=%q, want code=%q token=%q", code, token, tt.wantCode, tt.wantToken)
			}
		})
	}
}

func TestDevinAuthenticatorHeadlessAuthURL(t *testing.T) {
	authenticator := NewDevinAuthenticator()
	out := captureStdout(t, func() {
		_, _ = authenticator.Login(context.Background(), &config.Config{}, &LoginOptions{
			NoBrowser: true,
			Prompt:    func(string) (string, error) { return "", nil },
		})
	})
	var authURL string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "/auth/cli/continue?") {
			authURL = strings.TrimSpace(line)
			break
		}
	}
	if authURL == "" {
		t.Fatalf("printed output did not contain auth URL:\n%s", out)
	}
	parsed, errParse := url.Parse(authURL)
	if errParse != nil {
		t.Fatal(errParse)
	}
	q := parsed.Query()
	if q.Get("redirect_uri") != "" {
		t.Fatalf("headless URL still has redirect_uri=%q", q.Get("redirect_uri"))
	}
	if q.Get("cli_pkce_marker") != "1" {
		t.Fatalf("cli_pkce_marker=%q", q.Get("cli_pkce_marker"))
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("prompt") != "select_account" || q.Get("state") == "" {
		t.Fatalf("unexpected query: %s", parsed.RawQuery)
	}
	if !strings.HasSuffix(parsed.RawQuery, "&cli_pkce_marker=1") || strings.Contains(parsed.RawQuery, "redirect_uri=") {
		t.Fatalf("raw query order/content mismatch: %s", parsed.RawQuery)
	}
}

func TestDevinAuthenticatorHeadlessStateMismatch(t *testing.T) {
	var exchanged bool
	authSvc := newTestDevinAuthService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/cli/token" {
			exchanged = true
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	authenticator := NewDevinAuthenticator()
	authenticator.AuthService = authSvc

	_, err := authenticator.Login(context.Background(), &config.Config{}, &LoginOptions{
		NoBrowser: true,
		Prompt: func(string) (string, error) {
			return "http://127.0.0.1:9/callback?code=stolen-code&state=other-session", nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "state mismatch") {
		t.Fatalf("error = %v, want state mismatch", err)
	}
	if exchanged {
		t.Fatal("token exchange should not run on CSRF mismatch")
	}
}

func TestDevinAuthenticatorHeadlessOAuthErrorURL(t *testing.T) {
	authenticator := NewDevinAuthenticator()
	_, err := authenticator.Login(context.Background(), &config.Config{}, &LoginOptions{
		NoBrowser: true,
		Prompt: func(string) (string, error) {
			return "http://127.0.0.1:9/callback?error=access_denied&error_description=user%20denied", nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("error = %v, want access_denied oauth error", err)
	}
}

func TestDevinAuthenticatorHeadlessQuotedCode(t *testing.T) {
	authSvc := newTestDevinAuthService(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/cli/token":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if body["code"] != "quoted-code-xyz" {
				t.Errorf("code = %q", body["code"])
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"eyJquoted.token"}`))
		case "/v3/self":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"user_name":"quoted-user"}`))
		case devinauth.DevinGetUserStatusPath:
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	authenticator := NewDevinAuthenticator()
	authenticator.AuthService = authSvc

	auth, err := authenticator.Login(context.Background(), &config.Config{}, &LoginOptions{
		NoBrowser: true,
		Prompt:    func(string) (string, error) { return `"quoted-code-xyz"`, nil },
	})
	if err != nil {
		t.Fatalf("quoted code login failed: %v", err)
	}
	if auth.Attributes["api_key"] != "devin-session-token$eyJquoted.token" {
		t.Fatalf("api_key = %q", auth.Attributes["api_key"])
	}
}

func newTestDevinAuthService(t *testing.T, handler http.HandlerFunc) *devinauth.DevinAuthService {
	t.Helper()
	mockServer := httptest.NewServer(handler)
	t.Cleanup(mockServer.Close)
	authSvc := devinauth.NewDevinAuthService(mockServer.Client())
	authSvc.SetAPIBaseURL(mockServer.URL)
	authSvc.SetServerBaseURL(mockServer.URL)
	return authSvc
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, errPipe := os.Pipe()
	if errPipe != nil {
		t.Fatal(errPipe)
	}
	old := os.Stdout
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	if _, errCopy := io.Copy(&buf, r); errCopy != nil {
		t.Fatal(errCopy)
	}
	_ = r.Close()
	return buf.String()
}
