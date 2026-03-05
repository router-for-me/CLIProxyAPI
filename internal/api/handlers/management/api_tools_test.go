package management

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

type memoryAuthStore struct {
	mu    sync.Mutex
	items map[string]*coreauth.Auth
}

func (s *memoryAuthStore) List(ctx context.Context) ([]*coreauth.Auth, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*coreauth.Auth, 0, len(s.items))
	for _, a := range s.items {
		out = append(out, a.Clone())
	}
	return out, nil
}

func (s *memoryAuthStore) Save(ctx context.Context, auth *coreauth.Auth) (string, error) {
	_ = ctx
	if auth == nil {
		return "", nil
	}
	s.mu.Lock()
	if s.items == nil {
		s.items = make(map[string]*coreauth.Auth)
	}
	s.items[auth.ID] = auth.Clone()
	s.mu.Unlock()
	return auth.ID, nil
}

func (s *memoryAuthStore) Delete(ctx context.Context, id string) error {
	_ = ctx
	s.mu.Lock()
	delete(s.items, id)
	s.mu.Unlock()
	return nil
}

func TestResolveTokenForAuth_Antigravity_RefreshesExpiredToken(t *testing.T) {
	var callCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
			t.Fatalf("unexpected content-type: %s", ct)
		}
		bodyBytes, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		values, err := url.ParseQuery(string(bodyBytes))
		if err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if values.Get("grant_type") != "refresh_token" {
			t.Fatalf("unexpected grant_type: %s", values.Get("grant_type"))
		}
		if values.Get("refresh_token") != "rt" {
			t.Fatalf("unexpected refresh_token: %s", values.Get("refresh_token"))
		}
		if values.Get("client_id") != antigravityOAuthClientID {
			t.Fatalf("unexpected client_id: %s", values.Get("client_id"))
		}
		if values.Get("client_secret") != antigravityOAuthClientSecret {
			t.Fatalf("unexpected client_secret")
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-token",
			"refresh_token": "rt2",
			"expires_in":    int64(3600),
			"token_type":    "Bearer",
		})
	}))
	t.Cleanup(srv.Close)

	originalURL := antigravityOAuthTokenURL
	antigravityOAuthTokenURL = srv.URL
	t.Cleanup(func() { antigravityOAuthTokenURL = originalURL })

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)

	auth := &coreauth.Auth{
		ID:       "antigravity-test.json",
		FileName: "antigravity-test.json",
		Provider: "antigravity",
		Metadata: map[string]any{
			"type":          "antigravity",
			"access_token":  "old-token",
			"refresh_token": "rt",
			"expires_in":    int64(3600),
			"timestamp":     time.Now().Add(-2 * time.Hour).UnixMilli(),
			"expired":       time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	h := &Handler{authManager: manager}
	token, err := h.resolveTokenForAuth(context.Background(), auth)
	if err != nil {
		t.Fatalf("resolveTokenForAuth: %v", err)
	}
	if token != "new-token" {
		t.Fatalf("expected refreshed token, got %q", token)
	}
	if callCount != 1 {
		t.Fatalf("expected 1 refresh call, got %d", callCount)
	}

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth in manager after update")
	}
	if got := tokenValueFromMetadata(updated.Metadata); got != "new-token" {
		t.Fatalf("expected manager metadata updated, got %q", got)
	}
}

func TestResolveTokenForAuth_Antigravity_SkipsRefreshWhenTokenValid(t *testing.T) {
	var callCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	originalURL := antigravityOAuthTokenURL
	antigravityOAuthTokenURL = srv.URL
	t.Cleanup(func() { antigravityOAuthTokenURL = originalURL })

	auth := &coreauth.Auth{
		ID:       "antigravity-valid.json",
		FileName: "antigravity-valid.json",
		Provider: "antigravity",
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "ok-token",
			"expired":      time.Now().Add(30 * time.Minute).Format(time.RFC3339),
		},
	}
	h := &Handler{}
	token, err := h.resolveTokenForAuth(context.Background(), auth)
	if err != nil {
		t.Fatalf("resolveTokenForAuth: %v", err)
	}
	if token != "ok-token" {
		t.Fatalf("expected existing token, got %q", token)
	}
	if callCount != 0 {
		t.Fatalf("expected no refresh calls, got %d", callCount)
	}
}
