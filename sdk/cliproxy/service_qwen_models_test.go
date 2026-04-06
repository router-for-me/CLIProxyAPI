package cliproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestServiceRegisterModelsForAuth_PrefersDynamicQwenModels(t *testing.T) {
	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       "qwen-auth-dynamic",
		Provider: "qwen",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{
			"qwen_models": []map[string]any{
				{"id": "qwen-dyn-only", "name": "Qwen Dyn Only"},
			},
		},
	}

	reg := registry.GetGlobalRegistry()
	reg.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)

	models := reg.GetModelsForClient(auth.ID)
	if len(models) != 1 {
		t.Fatalf("expected 1 dynamic model registered, got %d", len(models))
	}
	if models[0] == nil || models[0].ID != "qwen-dyn-only" {
		t.Fatalf("models[0] = %#v, want id=%q", models[0], "qwen-dyn-only")
	}
	if models[0].DisplayName != "Qwen Dyn Only" {
		t.Fatalf("models[0].DisplayName = %q, want %q", models[0].DisplayName, "Qwen Dyn Only")
	}
}

func TestServiceRegisterModelsForAuth_QwenFallsBackToStaticModelsWhenDynamicMissing(t *testing.T) {
	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       "qwen-auth-static",
		Provider: "qwen",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{},
	}

	reg := registry.GetGlobalRegistry()
	reg.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)

	static := registry.GetQwenModels()
	if len(static) == 0 {
		t.Fatal("expected static qwen models to be non-empty for fallback")
	}
	wantID := static[0].ID

	models := reg.GetModelsForClient(auth.ID)
	if len(models) == 0 {
		t.Fatal("expected fallback qwen models to be registered")
	}
	found := false
	for _, m := range models {
		if m != nil && m.ID == wantID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected fallback to include static model %q, got %#v", wantID, models)
	}
}

func TestServiceRegisterModelsForAuth_SyncsQwenModelsWhenDynamicMissing(t *testing.T) {
	var modelsHits int32
	var statusHits int32
	var mu sync.Mutex
	var gotPath string
	var gotCookie string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/models":
			atomic.AddInt32(&modelsHits, 1)
			mu.Lock()
			gotPath = r.URL.Path
			gotCookie = r.Header.Get("Cookie")
			mu.Unlock()
			_, _ = io.WriteString(w, `{"success":true,"data":{"data":[{"id":"qwen-dyn-only","name":"Qwen Dyn Only"}]}}`)
		case "/api/v2/users/status":
			atomic.AddInt32(&statusHits, 1)
			_, _ = io.WriteString(w, `{"success":true}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       "qwen-auth-sync",
		Provider: "qwen",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  srv.URL,
		},
		Metadata: map[string]any{
			"token_cookie": "token-cookie",
		},
	}

	reg := registry.GetGlobalRegistry()
	reg.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)

	if atomic.LoadInt32(&modelsHits) != 1 {
		t.Fatalf("expected 1 request to qwen v2 models endpoint, got %d", modelsHits)
	}
	mu.Lock()
	if gotPath != "/api/v2/models" {
		mu.Unlock()
		t.Fatalf("path = %q, want %q", gotPath, "/api/v2/models")
	}
	if !strings.Contains(gotCookie, "token=token-cookie") {
		mu.Unlock()
		t.Fatalf("cookie header = %q, want token cookie", gotCookie)
	}
	mu.Unlock()
	// Optional: the implementation may also best-effort fetch /api/v2/users/status.
	if v := atomic.LoadInt32(&statusHits); v < 0 || v > 1 {
		t.Fatalf("unexpected status endpoint hits: %d", v)
	}
	if auth.Metadata == nil || auth.Metadata["qwen_models"] == nil {
		t.Fatalf("auth.Metadata[qwen_models] = %#v, want non-nil after sync", auth.Metadata)
	}

	models := reg.GetModelsForClient(auth.ID)
	if len(models) != 1 {
		t.Fatalf("expected 1 synced model registered, got %d", len(models))
	}
	if models[0] == nil || models[0].ID != "qwen-dyn-only" {
		t.Fatalf("models[0] = %#v, want id=%q", models[0], "qwen-dyn-only")
	}
	if models[0].DisplayName != "Qwen Dyn Only" {
		t.Fatalf("models[0].DisplayName = %q, want %q", models[0].DisplayName, "Qwen Dyn Only")
	}
}

func TestServiceRegisterModelsForAuth_QwenSyncSkipsWhenTokenCookieMissingAndFallsBack(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       "qwen-auth-no-cookie",
		Provider: "qwen",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  srv.URL,
		},
		Metadata: map[string]any{},
	}

	reg := registry.GetGlobalRegistry()
	reg.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)

	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("expected no qwen v2 sync request when token_cookie missing, got %d", hits)
	}

	static := registry.GetQwenModels()
	if len(static) == 0 {
		t.Fatal("expected static qwen models to be non-empty for fallback")
	}
	wantID := static[0].ID

	models := reg.GetModelsForClient(auth.ID)
	if len(models) == 0 {
		t.Fatal("expected fallback qwen models to be registered")
	}
	found := false
	for _, m := range models {
		if m != nil && m.ID == wantID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected fallback to include static model %q, got %#v", wantID, models)
	}
}
