package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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
			"device_code":      "dev123",
			"user_code":        "ABCD-EFGH",
			"verification_uri": "https://github.com/login/device",
			"expires_in":       5,
			"interval":         1,
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
			"token":      "copilot_token_abc",
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

	// Verify a copilot auth file is created with refresh_in, expires_at and github_access_token
	entries, err := os.ReadDir(cfg.AuthDir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		full := filepath.Join(cfg.AuthDir, e.Name())
		data, errR := os.ReadFile(full)
		if errR != nil {
			t.Fatalf("read file: %v", errR)
		}
		var js map[string]any
		if err := json.Unmarshal(data, &js); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if js["type"] == "copilot" {
			// keys should be present
			if _, ok := js["refresh_in"]; !ok {
				t.Fatalf("missing refresh_in in saved auth file")
			}
			if _, ok := js["expires_at"]; !ok {
				t.Fatalf("missing expires_at in saved auth file")
			}
			if v, ok := js["github_access_token"].(string); !ok || v == "" {
				t.Fatalf("missing github_access_token in saved auth file")
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a copilot auth json saved to %s", cfg.AuthDir)
	}
}
