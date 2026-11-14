package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"golang.org/x/crypto/bcrypt"
)

func TestSIWCManagementRouteAccessControl(t *testing.T) {
	hash, errHash := bcrypt.GenerateFromPassword([]byte("test-password"), bcrypt.MinCost)
	if errHash != nil {
		t.Fatal(errHash)
	}
	for _, tc := range []struct {
		name                      string
		enabled, home, authorized bool
		want                      int
	}{
		{"authorized invalid target", true, false, true, http.StatusBadRequest},
		{"missing management key", true, false, false, http.StatusUnauthorized},
		{"management disabled", false, false, true, http.StatusNotFound},
		{"home disabled local API", true, true, true, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{AuthDir: t.TempDir()}
			cfg.Home.Enabled = tc.home
			cfg.RemoteManagement.SecretKey = string(hash)
			h := management.NewHandlerWithoutConfigFilePath(cfg, nil)
			h.SetLocalPassword("test-password")
			s := &Server{cfg: cfg, engine: gin.New(), mgmt: h}
			s.managementRoutesEnabled.Store(tc.enabled)
			s.registerManagementRoutes()
			req := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/siwc/bind", strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = "127.0.0.1:1234"
			if tc.authorized {
				req.Header.Set("Authorization", "Bearer test-password")
			}
			w := httptest.NewRecorder()
			s.engine.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d: %s", w.Code, tc.want, w.Body.String())
			}
			for _, route := range s.engine.Routes() {
				if strings.HasPrefix(route.Path, "/v0/") && strings.Contains(route.Path, "siwc") {
					t.Fatal("SIWC route registered under v0")
				}
			}
		})
	}
}

func TestCodexCallbackRejectsSIWCState(t *testing.T) {
	hash, errHash := bcrypt.GenerateFromPassword([]byte("test-password"), bcrypt.MinCost)
	if errHash != nil {
		t.Fatal(errHash)
	}
	cfg := &config.Config{}
	cfg.RemoteManagement.SecretKey = string(hash)
	s := newTestServerWithConfig(t, cfg)
	metadata := map[string]any{"type": "codex", "access_token": "outer-token", "refresh_token": "outer-refresh"}
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.cfg.AuthDir, "codex.json")
	if errWrite := os.WriteFile(path, data, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(s.cfg)
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID: "codex.json", FileName: "codex.json", Provider: "codex", Status: coreauth.StatusActive,
		Metadata: metadata, Attributes: map[string]string{"path": path},
	}); errRegister != nil {
		t.Fatal(errRegister)
	}
	s.mgmt.SetAuthManager(manager)
	s.mgmt.SetLocalPassword("test-password")
	s.managementRoutesEnabled.Store(true)
	s.registerManagementRoutes()
	req := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/siwc/bind", strings.NewReader(`{"name":"codex.json"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-password")
	req.RemoteAddr = "127.0.0.1:1234"
	started := httptest.NewRecorder()
	s.engine.ServeHTTP(started, req)
	if started.Code != http.StatusOK {
		t.Fatalf("start status=%d: %s", started.Code, started.Body.String())
	}
	var flow struct {
		State string `json:"state"`
		Flow  string `json:"flow"`
	}
	if errDecode := json.Unmarshal(started.Body.Bytes(), &flow); errDecode != nil {
		t.Fatal(errDecode)
	}
	if flow.State == "" || flow.Flow != "siwc_bind" {
		t.Fatalf("unexpected bind response: %s", started.Body.String())
	}
	t.Cleanup(func() { management.CancelOAuthSession(flow.State) })
	callback := httptest.NewRecorder()
	query := url.Values{"state": {flow.State}, "code": {"issued-code"}, "client_id": {"oaiapp_issued"}}
	s.engine.ServeHTTP(callback, httptest.NewRequest(http.MethodGet, "/codex/callback?"+query.Encode(), nil))
	if callback.Code != http.StatusConflict {
		t.Fatalf("callback status=%d want=409: %s", callback.Code, callback.Body.String())
	}
	if !strings.Contains(callback.Body.String(), "/v8/management/oauth/callback") {
		t.Fatalf("missing v8 callback guidance: %s", callback.Body.String())
	}
	entries, errRead := os.ReadDir(s.cfg.AuthDir)
	if errRead != nil {
		t.Fatal(errRead)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".oauth") {
			t.Fatalf("legacy callback created OAuth file %q", entry.Name())
		}
	}
	if !management.IsOAuthSessionPending(flow.State, "codex") {
		t.Fatal("rejected callback consumed SIWC session")
	}
}

func TestSIWCStatusEndpoint(t *testing.T) {
	hash, errHash := bcrypt.GenerateFromPassword([]byte("test-password"), bcrypt.MinCost)
	if errHash != nil {
		t.Fatal(errHash)
	}
	cfg := &config.Config{}
	cfg.RemoteManagement.SecretKey = string(hash)
	s := newTestServerWithConfig(t, cfg)
	metadata := map[string]any{"type": "codex", "access_token": "outer-token", "refresh_token": "outer-refresh"}
	path := filepath.Join(s.cfg.AuthDir, "codex.json")
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if errWrite := os.WriteFile(path, data, 0o600); err != nil || errWrite != nil {
		t.Fatal(errWrite)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(s.cfg)
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID: "codex.json", FileName: "codex.json", Provider: "codex", Status: coreauth.StatusActive,
		Metadata: metadata, Attributes: map[string]string{"path": path},
	}); errRegister != nil {
		t.Fatal(errRegister)
	}
	s.mgmt.SetAuthManager(manager)
	s.mgmt.SetLocalPassword("test-password")
	s.managementRoutesEnabled.Store(true)
	s.registerManagementRoutes()

	checkStatus := func(url string, wantCode int, wantBody string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Authorization", "Bearer test-password")
		req.RemoteAddr = "127.0.0.1:1234"
		w := httptest.NewRecorder()
		s.engine.ServeHTTP(w, req)
		if w.Code != wantCode {
			t.Fatalf("%s: status=%d want=%d: %s", url, w.Code, wantCode, w.Body.String())
		}
		if wantBody != "" && strings.TrimSpace(w.Body.String()) != wantBody {
			t.Fatalf("%s: body=%s want %s", url, w.Body.String(), wantBody)
		}
	}

	// 1. use_siwc not enabled -> off
	checkStatus("/v8/management/oauth/siwc/status?name=codex.json", http.StatusOK, `{"status":"off"}`)
	checkStatus("/v8/management/credentials/siwc/status?name=codex.json", http.StatusOK, `{"status":"off"}`)

	// 2. Bind SIWC with valid future expires_at -> valid
	registered, _ := manager.GetByID("codex.json")
	_, errBind := manager.BindCodexSIWC(context.Background(), "codex.json", registered.RegistrationEpoch, "", &codex.CodexSIWCTokenData{
		AccessToken:  "siwc-valid-1",
		RefreshToken: "siwc-refresh-1",
		ClientID:     "oaiapp_1",
		Expire:       "2035-01-01T00:00:00Z",
		ExpiresAt:    "2035-01-01T00:03:00Z",
	})
	if errBind != nil {
		t.Fatalf("BindCodexSIWC: %v", errBind)
	}
	bound, _ := manager.GetByID("codex.json")
	bound.EnsureIndex()
	checkStatus("/v8/management/oauth/siwc/status?name=codex.json", http.StatusOK, `{"status":"valid"}`)
	checkStatus("/v8/management/oauth/siwc/status?auth_index="+bound.Index, http.StatusOK, `{"status":"valid"}`)

	// 3. SIWC request receives 401 -> invalid
	manager.MarkResult(context.Background(), coreauth.Result{
		AuthID:            "codex.json",
		CredentialVersion: bound.CredentialVersion,
		Provider:          "codex",
		Model:             "gpt-5.4",
		Success:           false,
		Error:             &coreauth.Error{HTTPStatus: http.StatusUnauthorized, Code: "unauthorized", Message: "401 unauthorized"},
	})
	checkStatus("/v8/management/oauth/siwc/status?name=codex.json", http.StatusOK, `{"status":"invalid"}`)

	// 4. Missing parameter or unknown target
	checkStatus("/v8/management/oauth/siwc/status", http.StatusBadRequest, "")
	checkStatus("/v8/management/oauth/siwc/status?name=missing.json", http.StatusNotFound, "")
}
