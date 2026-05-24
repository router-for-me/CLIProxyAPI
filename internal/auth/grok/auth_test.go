package grok

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestGenerateAuthURL_IncludesMandatoryParams asserts that the returned URL
// contains every required OAuth parameter, including the xAI-mandatory
// plan=generic and referrer=cliproxyapi fields.
func TestGenerateAuthURL_IncludesMandatoryParams(t *testing.T) {
	pkce := &PKCECodes{
		CodeVerifier:  "test-verifier-abc123",
		CodeChallenge: "test-challenge-xyz789",
	}

	g := NewGrokAuth(nil)
	rawURL, err := g.GenerateAuthURL("test-state", "test-nonce", pkce)
	if err != nil {
		t.Fatalf("GenerateAuthURL returned unexpected error: %v", err)
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("returned URL is not valid: %v", err)
	}

	q := parsed.Query()

	checks := map[string]string{
		"response_type":         "code",
		"client_id":             ClientID,
		"redirect_uri":          RedirectURI,
		"scope":                 Scope,
		"code_challenge":        pkce.CodeChallenge,
		"code_challenge_method": "S256",
		"state":                 "test-state",
		"nonce":                 "test-nonce",
		"plan":                  AuthorizePlan,     // must be "generic"
		"referrer":              AuthorizeReferrer, // must be "cliproxyapi"
	}

	for param, want := range checks {
		got := q.Get(param)
		if got != want {
			t.Errorf("param %q: got %q, want %q", param, got, want)
		}
	}

	// Confirm the base URL is the xAI authorize endpoint.
	wantBase := AuthorizeURL
	gotBase := parsed.Scheme + "://" + parsed.Host + parsed.Path
	if gotBase != wantBase {
		t.Errorf("base URL: got %q, want %q", gotBase, wantBase)
	}
}

// TestGenerateAuthURL_NilPKCEReturnsError verifies that passing nil PKCE codes
// produces an error rather than a panic or partial URL.
func TestGenerateAuthURL_NilPKCEReturnsError(t *testing.T) {
	g := NewGrokAuth(nil)
	_, err := g.GenerateAuthURL("some-state", "some-nonce", nil)
	if err == nil {
		t.Fatal("expected error for nil PKCECodes, got nil")
	}
	if !strings.Contains(err.Error(), "PKCE") {
		t.Errorf("error message should mention PKCE, got: %v", err)
	}
}

// TestExchangeCodeForTokens_ParsesSuccess stands up a fake token server,
// calls ExchangeCodeForTokens, and checks that the returned TokenResponse
// fields are populated correctly.
func TestExchangeCodeForTokens_ParsesSuccess(t *testing.T) {
	want := TokenResponse{
		AccessToken:  "access-tok-abc",
		RefreshToken: "refresh-tok-xyz",
		IDToken:      "id-tok-def",
		TokenType:    "Bearer",
		ExpiresIn:    3600,
		Scope:        Scope,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type: got %q, want application/x-www-form-urlencoded", ct)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("failed to parse form: %v", err)
		}
		if gt := r.FormValue("grant_type"); gt != "authorization_code" {
			t.Errorf("grant_type: got %q, want authorization_code", gt)
		}
		if cv := r.FormValue("code_verifier"); cv != "verifier-123" {
			t.Errorf("code_verifier: got %q, want verifier-123", cv)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(want)
	}))
	defer srv.Close()

	// Temporarily override TokenURL via a custom transport that redirects the
	// fixed TokenURL to our test server.
	g := &GrokAuth{
		httpClient: srv.Client(),
	}

	// We can't override the const, so we use a round-tripper that rewrites the host.
	g.httpClient.Transport = &rewriteTransport{
		base:    srv.Client().Transport,
		fromURL: TokenURL,
		toURL:   srv.URL,
	}

	pkce := &PKCECodes{
		CodeVerifier:  "verifier-123",
		CodeChallenge: "challenge-456",
	}

	got, err := g.ExchangeCodeForTokens(context.Background(), "auth-code-abc", pkce)
	if err != nil {
		t.Fatalf("ExchangeCodeForTokens returned unexpected error: %v", err)
	}
	if got.AccessToken != want.AccessToken {
		t.Errorf("AccessToken: got %q, want %q", got.AccessToken, want.AccessToken)
	}
	if got.RefreshToken != want.RefreshToken {
		t.Errorf("RefreshToken: got %q, want %q", got.RefreshToken, want.RefreshToken)
	}
	if got.TokenType != want.TokenType {
		t.Errorf("TokenType: got %q, want %q", got.TokenType, want.TokenType)
	}
	if got.ExpiresIn != want.ExpiresIn {
		t.Errorf("ExpiresIn: got %d, want %d", got.ExpiresIn, want.ExpiresIn)
	}
}

// TestExchangeCodeForTokens_HandlesNonOK verifies that a non-2xx response from
// the token endpoint is returned as a wrapped error containing the status code.
func TestExchangeCodeForTokens_HandlesNonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant","error_description":"code expired"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	g := &GrokAuth{httpClient: srv.Client()}
	g.httpClient.Transport = &rewriteTransport{
		base:    srv.Client().Transport,
		fromURL: TokenURL,
		toURL:   srv.URL,
	}

	pkce := &PKCECodes{CodeVerifier: "v", CodeChallenge: "c"}
	_, err := g.ExchangeCodeForTokens(context.Background(), "bad-code", pkce)
	if err == nil {
		t.Fatal("expected error for 400 response, got nil")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "400") {
		t.Errorf("error should contain status code 400, got: %s", errStr)
	}
}

// rewriteTransport redirects requests whose URL starts with fromURL to toURL.
type rewriteTransport struct {
	base    http.RoundTripper
	fromURL string
	toURL   string
}

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasPrefix(req.URL.String(), rt.fromURL) {
		newURL, err := url.Parse(rt.toURL + req.URL.RequestURI())
		if err != nil {
			return nil, err
		}
		req = req.Clone(req.Context())
		req.URL = newURL
		req.Host = newURL.Host
	}
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}
