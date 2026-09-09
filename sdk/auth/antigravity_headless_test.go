package auth_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
)

type mockRoundTripper func(req *http.Request) (*http.Response, error)

func (m mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return m(req)
}

func TestAntigravityConstantsAndURLBuilder(t *testing.T) {
	if auth.AntigravityDefaultCallbackPort != 51121 {
		t.Fatalf("expected callback port 51121, got %d", auth.AntigravityDefaultCallbackPort)
	}

	expectedDefaultURI := "http://localhost:51121/oauth-callback"
	if got := auth.AntigravityDefaultCallbackURI(); got != expectedDefaultURI {
		t.Fatalf("expected default callback URI %q, got %q", expectedDefaultURI, got)
	}

	// Test with custom redirect URI
	customURI := "https://admin.example.com/oauth/antigravity/callback"
	authURL := auth.BuildAntigravityAuthURL("state-123", customURI)
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("failed to parse auth URL: %v", err)
	}
	q := parsed.Query()
	if q.Get("state") != "state-123" {
		t.Fatalf("expected state=state-123, got %q", q.Get("state"))
	}
	if q.Get("redirect_uri") != customURI {
		t.Fatalf("expected redirect_uri=%s, got %q", customURI, q.Get("redirect_uri"))
	}
	if q.Get("response_type") != "code" {
		t.Fatalf("expected response_type=code, got %q", q.Get("response_type"))
	}
	if q.Get("access_type") != "offline" {
		t.Fatalf("expected access_type=offline, got %q", q.Get("access_type"))
	}
	if q.Get("prompt") != "consent" {
		t.Fatalf("expected prompt=consent, got %q", q.Get("prompt"))
	}
	if !strings.Contains(q.Get("scope"), "cloud-platform") {
		t.Fatalf("scope should include cloud-platform: %s", q.Get("scope"))
	}

	// Test with empty redirect URI (should fallback to default)
	authURLDefault := auth.BuildAntigravityAuthURL("state-456", "")
	parsedDefault, err := url.Parse(authURLDefault)
	if err != nil {
		t.Fatalf("failed to parse auth URL: %v", err)
	}
	if parsedDefault.Query().Get("redirect_uri") != expectedDefaultURI {
		t.Fatalf("expected default redirect URI fallback, got %q", parsedDefault.Query().Get("redirect_uri"))
	}
}

func TestAntigravityUserAgent(t *testing.T) {
	ua := auth.AntigravityUserAgent()
	if !strings.HasPrefix(ua, "antigravity/hub/") {
		t.Fatalf("expected AntigravityUserAgent to start with antigravity/hub/, got %q", ua)
	}
}

func TestExchangeAntigravityCode(t *testing.T) {
	client := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost || req.URL.String() != "https://oauth2.googleapis.com/token" {
				t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
			}
			bodyBytes, _ := io.ReadAll(req.Body)
			bodyStr := string(bodyBytes)
			if !strings.Contains(bodyStr, "code=auth-code-123") {
				t.Fatalf("request body missing code: %s", bodyStr)
			}
			if !strings.Contains(bodyStr, "grant_type=authorization_code") {
				t.Fatalf("request body missing grant_type: %s", bodyStr)
			}
			respJSON := `{"access_token":"mock-access-token","refresh_token":"mock-refresh-token","expires_in":3600,"token_type":"Bearer"}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(respJSON)),
			}, nil
		}),
	}

	tokenResp, err := auth.ExchangeAntigravityCode(context.Background(), "auth-code-123", "http://localhost:51121/oauth-callback", client)
	if err != nil {
		t.Fatalf("ExchangeAntigravityCode error: %v", err)
	}
	if tokenResp.AccessToken != "mock-access-token" {
		t.Fatalf("AccessToken = %q, want mock-access-token", tokenResp.AccessToken)
	}
	if tokenResp.RefreshToken != "mock-refresh-token" {
		t.Fatalf("RefreshToken = %q, want mock-refresh-token", tokenResp.RefreshToken)
	}
	if tokenResp.ExpiresIn != 3600 {
		t.Fatalf("ExpiresIn = %d, want 3600", tokenResp.ExpiresIn)
	}
}

func TestFetchAntigravityUserInfo(t *testing.T) {
	client := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodGet || !strings.HasPrefix(req.URL.String(), "https://www.googleapis.com/oauth2/v2/userinfo") {
				t.Fatalf("unexpected request: %s %s", req.Method, req.URL.String())
			}
			if req.Header.Get("Authorization") != "Bearer mock-access-token" {
				t.Fatalf("unexpected Authorization header: %s", req.Header.Get("Authorization"))
			}
			respJSON := `{"email":"engineer@example.com"}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(respJSON)),
			}, nil
		}),
	}

	email, err := auth.FetchAntigravityUserInfo(context.Background(), "mock-access-token", client)
	if err != nil {
		t.Fatalf("FetchAntigravityUserInfo error: %v", err)
	}
	if email != "engineer@example.com" {
		t.Fatalf("email = %q, want engineer@example.com", email)
	}
}

func TestBuildAntigravityAuth(t *testing.T) {
	tokenResp := &auth.AntigravityTokenResponse{
		AccessToken:  "mock-access-token",
		RefreshToken: "mock-refresh-token",
		ExpiresIn:    3600,
		TokenType:    "Bearer",
	}

	record := auth.BuildAntigravityAuth(tokenResp, "engineer@example.com", "my-gcp-project")
	if record == nil {
		t.Fatal("BuildAntigravityAuth returned nil")
	}
	if record.Provider != "antigravity" {
		t.Fatalf("Provider = %q, want antigravity", record.Provider)
	}
	if record.ID != "antigravity-engineer@example.com.json" {
		t.Fatalf("ID = %q", record.ID)
	}
	if record.FileName != "antigravity-engineer@example.com.json" {
		t.Fatalf("FileName = %q", record.FileName)
	}
	if record.Label != "engineer@example.com" {
		t.Fatalf("Label = %q", record.Label)
	}
	meta := record.Metadata
	if meta["type"] != "antigravity" {
		t.Fatalf("Metadata[type] = %v", meta["type"])
	}
	if meta["access_token"] != "mock-access-token" {
		t.Fatalf("Metadata[access_token] = %v", meta["access_token"])
	}
	if meta["refresh_token"] != "mock-refresh-token" {
		t.Fatalf("Metadata[refresh_token] = %v", meta["refresh_token"])
	}
	if meta["email"] != "engineer@example.com" {
		t.Fatalf("Metadata[email] = %v", meta["email"])
	}
	if meta["project_id"] != "my-gcp-project" {
		t.Fatalf("Metadata[project_id] = %v", meta["project_id"])
	}
	if _, ok := meta["timestamp"].(int64); !ok {
		t.Fatalf("Metadata[timestamp] missing or wrong type: %v", meta["timestamp"])
	}
	if expiredStr, ok := meta["expired"].(string); !ok || expiredStr == "" {
		t.Fatalf("Metadata[expired] missing: %v", meta["expired"])
	}
}

func TestCompleteAntigravityOAuth(t *testing.T) {
	client := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
			switch {
			case strings.HasPrefix(req.URL.String(), "https://oauth2.googleapis.com/token"):
				respJSON := `{"access_token":"mock-token","refresh_token":"mock-refresh","expires_in":3600,"token_type":"Bearer"}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(respJSON)),
				}, nil
			case strings.HasPrefix(req.URL.String(), "https://www.googleapis.com/oauth2/v2/userinfo"):
				respJSON := `{"email":"complete@example.com"}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(respJSON)),
				}, nil
			case strings.Contains(req.URL.String(), "loadCodeAssist"):
				respJSON := `{"cloudaicompanionProject":"complete-proj-123"}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(respJSON)),
				}, nil
			default:
				t.Fatalf("unexpected mock URL: %s", req.URL.String())
				return nil, nil
			}
		}),
	}

	record, err := auth.CompleteAntigravityOAuth(context.Background(), "auth-code-xyz", "http://localhost:51121/oauth-callback", client)
	if err != nil {
		t.Fatalf("CompleteAntigravityOAuth error: %v", err)
	}
	if record == nil {
		t.Fatal("expected non-nil Auth record")
	}
	if record.Label != "complete@example.com" {
		t.Fatalf("Label = %q, want complete@example.com", record.Label)
	}
	if record.Metadata["project_id"] != "complete-proj-123" {
		t.Fatalf("Metadata[project_id] = %v, want complete-proj-123", record.Metadata["project_id"])
	}
}

func TestExchangeAntigravityCodeDefaultRedirectAndErrors(t *testing.T) {
	// Verify default redirect URI fallback during exchange
	var capturedBody string
	client := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
			b, _ := io.ReadAll(req.Body)
			capturedBody = string(b)
			respJSON := `{"access_token":"mock-token","refresh_token":"mock-refresh","expires_in":3600,"token_type":"Bearer"}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(respJSON)),
			}, nil
		}),
	}

	_, err := auth.ExchangeAntigravityCode(context.Background(), "code-123", "", client)
	if err != nil {
		t.Fatalf("ExchangeAntigravityCode error: %v", err)
	}
	expectedRedirect := url.QueryEscape(auth.AntigravityDefaultCallbackURI())
	if !strings.Contains(capturedBody, expectedRedirect) {
		t.Fatalf("expected default redirect %q in body: %s", expectedRedirect, capturedBody)
	}

	// Verify upstream error response
	errorClient := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":"invalid_grant"}`)),
			}, nil
		}),
	}
	_, err = auth.ExchangeAntigravityCode(context.Background(), "bad-code", "", errorClient)
	if err == nil {
		t.Fatal("expected error on upstream 400 response")
	}

	// Verify context cancellation
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	contextAwareClient := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
			if errCtx := req.Context().Err(); errCtx != nil {
				return nil, errCtx
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{}`)),
			}, nil
		}),
	}
	_, err = auth.ExchangeAntigravityCode(canceledCtx, "code", "", contextAwareClient)
	if err == nil {
		t.Fatal("expected error with canceled context")
	}
}

func TestFetchAntigravityUserInfoErrors(t *testing.T) {
	// Empty token check
	_, err := auth.FetchAntigravityUserInfo(context.Background(), "", nil)
	if err == nil {
		t.Fatal("expected error with empty access token")
	}

	// Upstream error check
	errorClient := &http.Client{
		Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":"invalid_token"}`)),
			}, nil
		}),
	}
	_, err = auth.FetchAntigravityUserInfo(context.Background(), "invalid-token", errorClient)
	if err == nil {
		t.Fatal("expected error on upstream 401 response")
	}
}

func TestBuildAntigravityAuthNilCheck(t *testing.T) {
	if auth.BuildAntigravityAuth(nil, "a@b.com", "proj") != nil {
		t.Fatal("expected nil return for nil token response")
	}
}
