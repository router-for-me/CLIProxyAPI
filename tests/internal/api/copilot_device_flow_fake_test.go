package api_test

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "path/filepath"
    "testing"
    "time"

    "github.com/gin-gonic/gin"
    management "github.com/router-for-me/CLIProxyAPI/v6/internal/api/handlers/management"
    "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// Fake GitHub + Copilot endpoints to drive device flow without network.
func TestCopilotDeviceFlow_FakeServer_E2E_Min(t *testing.T) {
    gin.SetMode(gin.TestMode)

    // Fake upstream server
    mux := http.NewServeMux()
    mux.HandleFunc("/login/device/code", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        _ = json.NewEncoder(w).Encode(map[string]any{
            "device_code": "dev123",
            "user_code": "ABCD-EFGH",
            "verification_uri": "https://github.com/login/device",
            "expires_in": 5,
            "interval": 1,
        })
    })
    mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        _ = json.NewEncoder(w).Encode(map[string]any{
            "access_token": "gh_pat_123",
        })
    })
    mux.HandleFunc("/copilot_internal/v2/token", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        _ = json.NewEncoder(w).Encode(map[string]any{
            "token": "copilot_token_abc",
            "expires_at": time.Now().Add(1 * time.Hour).UnixMilli(),
            "refresh_in": 3600,
        })
    })
    fake := httptest.NewServer(mux)
    defer fake.Close()

    cfg := &config.Config{AuthDir: t.TempDir()}
    cfg.Copilot.GitHubBaseURL = fake.URL
    cfg.Copilot.GitHubAPIBaseURL = fake.URL
    cfg.Copilot.GitHubClientID = "fake-client"

    h := management.NewHandler(cfg, filepath.Join(cfg.AuthDir, "config.yaml"), nil)
    r := gin.New()
    r.GET("/v0/management/copilot-device-code", h.RequestCopilotDeviceCode)

    req := httptest.NewRequest(http.MethodGet, "/v0/management/copilot-device-code", nil)
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", w.Code)
    }
    // allow background poller to run once
    time.Sleep(1500 * time.Millisecond)
}

