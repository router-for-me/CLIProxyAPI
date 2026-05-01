package codebuddy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestAuth creates a CodeBuddyAuth pointing at the given test server.
func newTestAuth(serverURL string) *CodeBuddyAuth {
	return &CodeBuddyAuth{
		httpClient: http.DefaultClient,
		baseURL:    serverURL,
	}
}

// fakeJWT builds a minimal JWT with the given sub claim for testing.
func fakeJWT(sub string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`))
	payload, _ := json.Marshal(map[string]any{"sub": sub, "iat": 1234567890})
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	return header + "." + encodedPayload + ".sig"
}

// --- FetchAuthState tests ---

func TestFetchAuthState_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if got := r.URL.Path; got != codeBuddyStatePath {
			t.Errorf("expected path %s, got %s", codeBuddyStatePath, got)
		}
		if got := r.URL.Query().Get("platform"); got != "CLI" {
			t.Errorf("expected platform=CLI, got %s", got)
		}
		if got := r.Header.Get("User-Agent"); got != UserAgent {
			t.Errorf("expected User-Agent %s, got %s", UserAgent, got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"msg":  "ok",
			"data": map[string]any{
				"state":   "test-state-abc",
				"authUrl": "https://example.com/login?state=test-state-abc",
			},
		})
	}))
	defer srv.Close()

	auth := newTestAuth(srv.URL)
	result, err := auth.FetchAuthState(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.State != "test-state-abc" {
		t.Errorf("expected state 'test-state-abc', got '%s'", result.State)
	}
	if result.AuthURL != "https://example.com/login?state=test-state-abc" {
		t.Errorf("unexpected authURL: %s", result.AuthURL)
	}
}

func TestFetchAuthState_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	auth := newTestAuth(srv.URL)
	_, err := auth.FetchAuthState(context.Background())
	if err == nil {
		t.Fatal("expected error for non-200 status")
	}
}

func TestFetchAuthState_APIErrorCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 10001,
			"msg":  "rate limited",
		})
	}))
	defer srv.Close()

	auth := newTestAuth(srv.URL)
	_, err := auth.FetchAuthState(context.Background())
	if err == nil {
		t.Fatal("expected error for non-zero code")
	}
}

func TestFetchAuthState_MissingData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"msg":  "ok",
			"data": map[string]any{
				"state":   "",
				"authUrl": "",
			},
		})
	}))
	defer srv.Close()

	auth := newTestAuth(srv.URL)
	_, err := auth.FetchAuthState(context.Background())
	if err == nil {
		t.Fatal("expected error for empty state/authUrl")
	}
}

// --- RefreshToken tests ---

func TestRefreshToken_Success(t *testing.T) {
	newAccessToken := fakeJWT("refreshed-user-456")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if got := r.URL.Path; got != codeBuddyRefreshPath {
			t.Errorf("expected path %s, got %s", codeBuddyRefreshPath, got)
		}
		if got := r.Header.Get("X-Refresh-Token"); got != "old-refresh-token" {
			t.Errorf("expected X-Refresh-Token 'old-refresh-token', got '%s'", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer old-access-token" {
			t.Errorf("expected Authorization 'Bearer old-access-token', got '%s'", got)
		}
		if got := r.Header.Get("X-User-Id"); got != "user-123" {
			t.Errorf("expected X-User-Id 'user-123', got '%s'", got)
		}
		if got := r.Header.Get("X-Domain"); got != "custom.domain.com" {
			t.Errorf("expected X-Domain 'custom.domain.com', got '%s'", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"msg":  "ok",
			"data": map[string]any{
				"accessToken":      newAccessToken,
				"refreshToken":     "new-refresh-token",
				"expiresIn":        3600,
				"refreshExpiresIn": 86400,
				"tokenType":        "bearer",
				"domain":           "custom.domain.com",
			},
		})
	}))
	defer srv.Close()

	auth := newTestAuth(srv.URL)
	storage, err := auth.RefreshToken(context.Background(), "old-access-token", "old-refresh-token", "user-123", "custom.domain.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if storage.AccessToken != newAccessToken {
		t.Errorf("expected new access token, got '%s'", storage.AccessToken)
	}
	if storage.RefreshToken != "new-refresh-token" {
		t.Errorf("expected 'new-refresh-token', got '%s'", storage.RefreshToken)
	}
	if storage.UserID != "refreshed-user-456" {
		t.Errorf("expected userID 'refreshed-user-456', got '%s'", storage.UserID)
	}
	if storage.ExpiresIn != 3600 {
		t.Errorf("expected expiresIn 3600, got %d", storage.ExpiresIn)
	}
	if storage.RefreshExpiresIn != 86400 {
		t.Errorf("expected refreshExpiresIn 86400, got %d", storage.RefreshExpiresIn)
	}
	if storage.Domain != "custom.domain.com" {
		t.Errorf("expected domain 'custom.domain.com', got '%s'", storage.Domain)
	}
	if storage.Type != "codebuddy" {
		t.Errorf("expected type 'codebuddy', got '%s'", storage.Type)
	}
}

func TestRefreshToken_DefaultDomain(t *testing.T) {
	var receivedDomain string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedDomain = r.Header.Get("X-Domain")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"msg":  "ok",
			"data": map[string]any{
				"accessToken":  fakeJWT("user-1"),
				"refreshToken": "rt",
				"expiresIn":    3600,
				"tokenType":    "bearer",
				"domain":       DefaultDomain,
			},
		})
	}))
	defer srv.Close()

	auth := newTestAuth(srv.URL)
	_, err := auth.RefreshToken(context.Background(), "at", "rt", "uid", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedDomain != DefaultDomain {
		t.Errorf("expected default domain '%s', got '%s'", DefaultDomain, receivedDomain)
	}
}

func TestRefreshToken_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	auth := newTestAuth(srv.URL)
	_, err := auth.RefreshToken(context.Background(), "at", "rt", "uid", "d")
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
}

func TestRefreshToken_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	auth := newTestAuth(srv.URL)
	_, err := auth.RefreshToken(context.Background(), "at", "rt", "uid", "d")
	if err == nil {
		t.Fatal("expected error for 403 response")
	}
}

func TestRefreshToken_APIErrorCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 40001,
			"msg":  "invalid refresh token",
		})
	}))
	defer srv.Close()

	auth := newTestAuth(srv.URL)
	_, err := auth.RefreshToken(context.Background(), "at", "rt", "uid", "d")
	if err == nil {
		t.Fatal("expected error for non-zero API code")
	}
}

func TestRefreshToken_FallbackUserIDAndDomain(t *testing.T) {
	// When the new access token cannot be decoded for userID, it should fall back to the provided one.
	// When the response domain is empty, it should fall back to the request domain.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"msg":  "ok",
			"data": map[string]any{
				"accessToken":  "not-a-valid-jwt",
				"refreshToken": "new-rt",
				"expiresIn":    7200,
				"tokenType":    "bearer",
				"domain":       "",
			},
		})
	}))
	defer srv.Close()

	auth := newTestAuth(srv.URL)
	storage, err := auth.RefreshToken(context.Background(), "at", "rt", "original-uid", "original.domain.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if storage.UserID != "original-uid" {
		t.Errorf("expected fallback userID 'original-uid', got '%s'", storage.UserID)
	}
	if storage.Domain != "original.domain.com" {
		t.Errorf("expected fallback domain 'original.domain.com', got '%s'", storage.Domain)
	}
}
