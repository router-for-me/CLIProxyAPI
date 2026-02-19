package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestNewCodexAuth(t *testing.T) {
	cfg := &config.Config{}
	auth := NewCodexAuth(cfg)
	if auth.httpClient == nil {
		t.Error("expected non-nil httpClient")
	}
}

func TestCodexAuth_GenerateAuthURL(t *testing.T) {
	auth := &CodexAuth{}
	pkce := &PKCECodes{CodeChallenge: "challenge"}
	state := "state123"

	url, err := auth.GenerateAuthURL(state, pkce)
	if err != nil {
		t.Fatalf("GenerateAuthURL failed: %v", err)
	}

	if !strings.Contains(url, "state=state123") {
		t.Errorf("URL missing state: %s", url)
	}
	if !strings.Contains(url, "code_challenge=challenge") {
		t.Errorf("URL missing code_challenge: %s", url)
	}

	_, err = auth.GenerateAuthURL(state, nil)
	if err == nil {
		t.Error("expected error for nil pkceCodes")
	}
}

func TestCodexAuth_ExchangeCodeForTokens(t *testing.T) {
	// Mock ID token payload
	claims := JWTClaims{
		Email: "test@example.com",
		CodexAuthInfo: CodexAuthInfo{
			ChatgptAccountID: "acc_123",
		},
	}
	payload, _ := json.Marshal(claims)
	idToken := "header." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("expected urlencoded content type, got %s", r.Header.Get("Content-Type"))
		}

		resp := struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			IDToken      string `json:"id_token"`
			TokenType    string `json:"token_type"`
			ExpiresIn    int    `json:"expires_in"`
		}{
			AccessToken:  "access",
			RefreshToken: "refresh",
			IDToken:      idToken,
			TokenType:    "Bearer",
			ExpiresIn:    3600,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Override TokenURL for testing if it was possible, but it's a constant.
	// Since I can't override the constant, I'll need to use a real CodexAuth but with a mocked httpClient that redirects to my server.
	
	mockClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			// Redirect all requests to the test server
			mockReq, _ := http.NewRequest(req.Method, server.URL, req.Body)
			mockReq.Header = req.Header
			return http.DefaultClient.Do(mockReq)
		}),
	}

	auth := &CodexAuth{httpClient: mockClient}
	pkce := &PKCECodes{CodeVerifier: "verifier"}

	bundle, err := auth.ExchangeCodeForTokens(context.Background(), "code", pkce)
	if err != nil {
		t.Fatalf("ExchangeCodeForTokens failed: %v", err)
	}

	if bundle.TokenData.AccessToken != "access" {
		t.Errorf("expected access token, got %s", bundle.TokenData.AccessToken)
	}
	if bundle.TokenData.Email != "test@example.com" {
		t.Errorf("expected email test@example.com, got %s", bundle.TokenData.Email)
	}
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestCodexAuth_RefreshTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			IDToken      string `json:"id_token"`
			TokenType    string `json:"token_type"`
			ExpiresIn    int    `json:"expires_in"`
		}{
			AccessToken:  "new_access",
			RefreshToken: "new_refresh",
			IDToken:      "header.eyBlbWFpbCI6InJlZnJlc2hAZXhhbXBsZS5jb20ifQ.sig", // email: refresh@example.com
			TokenType:    "Bearer",
			ExpiresIn:    3600,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	mockClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			mockReq, _ := http.NewRequest(req.Method, server.URL, req.Body)
			return http.DefaultClient.Do(mockReq)
		}),
	}

	auth := &CodexAuth{httpClient: mockClient}
	tokenData, err := auth.RefreshTokens(context.Background(), "old_refresh")
	if err != nil {
		t.Fatalf("RefreshTokens failed: %v", err)
	}

	if tokenData.AccessToken != "new_access" {
		t.Errorf("expected new_access, got %s", tokenData.AccessToken)
	}
}

func TestCodexAuth_CreateTokenStorage(t *testing.T) {
	auth := &CodexAuth{}
	bundle := &CodexAuthBundle{
		TokenData: CodexTokenData{
			IDToken:      "id",
			AccessToken:  "access",
			RefreshToken: "refresh",
			AccountID:    "acc",
			Email:        "test@example.com",
			Expire:       "exp",
		},
		LastRefresh: "last",
	}

	storage := auth.CreateTokenStorage(bundle)
	if storage.AccessToken != "access" || storage.Email != "test@example.com" {
		t.Errorf("CreateTokenStorage failed: %+v", storage)
	}
}

func TestCodexAuth_RefreshTokensWithRetry(t *testing.T) {
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if count < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		resp := struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int    `json:"expires_in"`
		}{
			AccessToken: "retry_access",
			ExpiresIn:   3600,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	mockClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			mockReq, _ := http.NewRequest(req.Method, server.URL, req.Body)
			return http.DefaultClient.Do(mockReq)
		}),
	}

	auth := &CodexAuth{httpClient: mockClient}
	tokenData, err := auth.RefreshTokensWithRetry(context.Background(), "refresh", 3)
	if err != nil {
		t.Fatalf("RefreshTokensWithRetry failed: %v", err)
	}

	if tokenData.AccessToken != "retry_access" {
		t.Errorf("expected retry_access, got %s", tokenData.AccessToken)
	}
	if count != 2 {
		t.Errorf("expected 2 attempts, got %d", count)
	}
}

func TestCodexAuth_UpdateTokenStorage(t *testing.T) {
	auth := &CodexAuth{}
	storage := &CodexTokenStorage{AccessToken: "old"}
	tokenData := &CodexTokenData{
		AccessToken: "new",
		Email:       "new@example.com",
	}

	auth.UpdateTokenStorage(storage, tokenData)
	if storage.AccessToken != "new" || storage.Email != "new@example.com" {
		t.Errorf("UpdateTokenStorage failed: %+v", storage)
	}
	if storage.LastRefresh == "" {
		t.Error("expected LastRefresh to be set")
	}
}
