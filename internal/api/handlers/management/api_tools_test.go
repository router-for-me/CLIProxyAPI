package management

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
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

func TestAPICall_RevivesCodexQuotaStateFromOfficialUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	now := time.Now().UTC()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token-123" {
			t.Fatalf("unexpected authorization header: %s", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plan_type": "team",
			"rate_limit": map[string]any{
				"allowed":       true,
				"limit_reached": false,
				"primary_window": map[string]any{
					"used_percent":         0,
					"limit_window_seconds": 18000,
					"reset_after_seconds":  120,
					"reset_at":             now.Add(2 * time.Minute).Unix(),
				},
				"secondary_window": map[string]any{
					"used_percent":         0,
					"limit_window_seconds": 604800,
					"reset_after_seconds":  3600,
					"reset_at":             now.Add(time.Hour).Unix(),
				},
			},
		})
	}))
	t.Cleanup(upstream.Close)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	auth := &coreauth.Auth{
		ID:       "codex-revive.json",
		FileName: filepath.Base("codex-revive.json"),
		Provider: "codex",
		Status:   coreauth.StatusError,
		Metadata: map[string]any{
			"access_token": "token-123",
			"email":        "revive@example.com",
		},
		Unavailable:    true,
		NextRetryAfter: now.Add(24 * time.Hour),
		Quota: coreauth.QuotaState{
			Exceeded:      true,
			Reason:        "quota",
			NextRecoverAt: now.Add(24 * time.Hour),
		},
		StatusMessage: `{"error":{"type":"usage_limit_reached"}}`,
		ModelStates: map[string]*coreauth.ModelState{
			"gpt-5.4": {
				Status:         coreauth.StatusError,
				StatusMessage:  `{"error":{"type":"usage_limit_reached"}}`,
				Unavailable:    true,
				NextRetryAfter: now.Add(24 * time.Hour),
				LastError:      &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: `{"error":{"type":"usage_limit_reached"}}`},
				Quota: coreauth.QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: now.Add(24 * time.Hour),
				},
			},
		},
	}
	registered, err := manager.Register(context.Background(), auth)
	if err != nil {
		t.Fatalf("register auth: %v", err)
	}

	h := &Handler{authManager: manager}
	reqBody := map[string]any{
		"auth_index": registered.Index,
		"method":     "GET",
		"url":        upstream.URL + "/backend-api/wham/usage",
		"header": map[string]string{
			"Authorization": "Bearer $TOKEN$",
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/api-call", strings.NewReader(string(bodyBytes)))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.APICall(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", rec.Code, rec.Body.String())
	}

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected updated auth")
	}
	if updated.Unavailable {
		t.Fatalf("expected auth to be available after usage refresh")
	}
	if !updated.NextRetryAfter.IsZero() {
		t.Fatalf("expected next_retry_after cleared, got %v", updated.NextRetryAfter)
	}
	if updated.Quota.Exceeded {
		t.Fatalf("expected auth quota to be cleared")
	}
	if updated.Status != coreauth.StatusActive {
		t.Fatalf("expected auth status active, got %s", updated.Status)
	}
	state := updated.ModelStates["gpt-5.4"]
	if state == nil {
		t.Fatal("expected model state to exist")
	}
	if state.Unavailable || state.Quota.Exceeded || !state.NextRetryAfter.IsZero() || state.Status != coreauth.StatusActive {
		t.Fatalf("expected model quota state cleared, got %#v", state)
	}
}

func TestAPICall_DoesNotReviveCodexQuotaStateWhenOfficialUsageStillBlocked(t *testing.T) {
	gin.SetMode(gin.TestMode)

	now := time.Now().UTC()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plan_type": "team",
			"rate_limit": map[string]any{
				"allowed":       false,
				"limit_reached": true,
				"secondary_window": map[string]any{
					"used_percent":         100,
					"limit_window_seconds": 604800,
					"reset_after_seconds":  3600,
					"reset_at":             now.Add(time.Hour).Unix(),
				},
			},
		})
	}))
	t.Cleanup(upstream.Close)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	auth := &coreauth.Auth{
		ID:             "codex-still-blocked.json",
		FileName:       filepath.Base("codex-still-blocked.json"),
		Provider:       "codex",
		Status:         coreauth.StatusError,
		Metadata:       map[string]any{"access_token": "token-123"},
		Unavailable:    true,
		NextRetryAfter: now.Add(24 * time.Hour),
		Quota: coreauth.QuotaState{
			Exceeded:      true,
			Reason:        "quota",
			NextRecoverAt: now.Add(24 * time.Hour),
		},
		StatusMessage: `{"error":{"type":"usage_limit_reached"}}`,
	}
	registered, err := manager.Register(context.Background(), auth)
	if err != nil {
		t.Fatalf("register auth: %v", err)
	}

	h := &Handler{authManager: manager}
	reqBody := map[string]any{
		"auth_index": registered.Index,
		"method":     "GET",
		"url":        upstream.URL + "/backend-api/wham/usage",
		"header": map[string]string{
			"Authorization": "Bearer $TOKEN$",
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/api-call", strings.NewReader(string(bodyBytes)))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.APICall(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", rec.Code, rec.Body.String())
	}

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected updated auth")
	}
	if !updated.Unavailable {
		t.Fatalf("expected auth to remain unavailable")
	}
	if updated.NextRetryAfter.IsZero() {
		t.Fatalf("expected next_retry_after to remain set")
	}
	if !updated.Quota.Exceeded {
		t.Fatalf("expected auth quota to remain exceeded")
	}
}
