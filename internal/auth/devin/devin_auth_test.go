package devin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPKCEGeneration(t *testing.T) {
	pkce, err := GeneratePKCECodes()
	if err != nil {
		t.Fatalf("GeneratePKCECodes failed: %v", err)
	}
	if len(pkce.CodeVerifier) < 43 {
		t.Errorf("code_verifier too short: %d", len(pkce.CodeVerifier))
	}
	if len(pkce.CodeChallenge) == 0 {
		t.Errorf("code_challenge is empty")
	}
	// Re-generation produces unique values
	pkce2, _ := GeneratePKCECodes()
	if pkce.CodeVerifier == pkce2.CodeVerifier {
		t.Error("expected randomly unique code verifiers")
	}
}

func TestFormatSessionToken(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{
			input: "devin-session-token$eyJ123",
			want:  "devin-session-token$eyJ123",
		},
		{
			input: "eyJ123.456.789",
			want:  "devin-session-token$eyJ123.456.789",
		},
		{
			input: "custom-token-xyz",
			want:  "custom-token-xyz",
		},
	}

	for _, tt := range tests {
		got := FormatSessionToken(tt.input)
		if got != tt.want {
			t.Errorf("FormatSessionToken(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBuildAuthorizationURL(t *testing.T) {
	svc := NewDevinAuthService(nil)
	u := svc.BuildAuthorizationURL("http://127.0.0.1:1234/callback", "test-challenge", "state-abc")

	if !strings.HasPrefix(u, "https://app.devin.ai/auth/cli/continue?") {
		t.Fatalf("unexpected url prefix: %s", u)
	}

	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatalf("failed to parse auth url: %v", err)
	}
	q := parsed.Query()
	if q.Get("redirect_uri") != "http://127.0.0.1:1234/callback" {
		t.Errorf("redirect_uri = %q", q.Get("redirect_uri"))
	}
	if q.Get("code_challenge") != "test-challenge" {
		t.Errorf("code_challenge = %q", q.Get("code_challenge"))
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q", q.Get("code_challenge_method"))
	}
	if q.Get("state") != "state-abc" {
		t.Errorf("state = %q", q.Get("state"))
	}
}

func TestExchangeCodeForTokenAndFetchSelf(t *testing.T) {
	// Mock devin API backend
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/cli/token":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if body["code"] != "valid-code" || body["code_verifier"] != "valid-verifier" {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"token":"eyJtest-jwt-token"}`))

		case "/v3/self":
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"principal_type": "windsurf_session",
				"user_id": "user-test-123",
				"user_name": "devin-user-abc",
				"org_id": "org-test-456"
			}`))

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockServer.Close()

	svc := NewDevinAuthService(mockServer.Client())
	svc.apiBaseURL = mockServer.URL

	ctx := context.Background()

	// 1. Exchange code
	token, err := svc.ExchangeCodeForToken(ctx, "valid-code", "valid-verifier")
	if err != nil {
		t.Fatalf("ExchangeCodeForToken failed: %v", err)
	}
	if token != "eyJtest-jwt-token" {
		t.Errorf("token = %q, want eyJtest-jwt-token", token)
	}

	// 2. Fetch self profile
	userName, userID, orgID, errSelf := svc.FetchSelfProfile(ctx, "devin-session-token$"+token)
	if errSelf != nil {
		t.Fatalf("FetchSelfProfile failed: %v", errSelf)
	}
	if userName != "devin-user-abc" {
		t.Errorf("userName = %q, want devin-user-abc", userName)
	}
	if userID != "user-test-123" {
		t.Errorf("userID = %q, want user-test-123", userID)
	}
	if orgID != "org-test-456" {
		t.Errorf("orgID = %q, want org-test-456", orgID)
	}
}

func TestOAuthServerCallback(t *testing.T) {
	server := NewOAuthServer(0)
	port, err := server.Start()
	if err != nil {
		t.Fatalf("OAuthServer.Start() failed: %v", err)
	}
	defer func() {
		_ = server.Stop(context.Background())
	}()

	// Simulate browser redirect to /callback?code=mock-code&state=mock-state
	client := &http.Client{}
	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/callback?code=mock-code&state=mock-state", port)

	resp, errGet := client.Get(callbackURL)
	if errGet != nil {
		t.Fatalf("GET /callback failed: %v", errGet)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	res, errWait := server.WaitForCallback(2 * time.Second)
	if errWait != nil {
		t.Fatalf("WaitForCallback failed: %v", errWait)
	}
	if res.Code != "mock-code" {
		t.Errorf("res.Code = %q, want mock-code", res.Code)
	}
	if res.State != "mock-state" {
		t.Errorf("res.State = %q, want mock-state", res.State)
	}
}
