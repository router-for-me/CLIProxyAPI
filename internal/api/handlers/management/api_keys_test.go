package management

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func newAPIKeysTestHandler(t *testing.T, cfg *config.Config) *Handler {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("api-keys: []\n"), 0o600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	return NewHandler(cfg, configPath, nil)
}

func TestPutAPIKeys_AcceptsLegacyAndStructuredEntries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	h := newAPIKeysTestHandler(t, cfg)

	r := gin.New()
	r.PUT("/v0/management/api-keys", h.PutAPIKeys)

	body := `["legacy-key", {"api-key":"structured-key","requests-per-second":9}]`
	req := httptest.NewRequest(http.MethodPut, "/v0/management/api-keys", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("PUT /api-keys status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if len(cfg.APIKeys) != 2 {
		t.Fatalf("len(cfg.APIKeys) = %d, want 2", len(cfg.APIKeys))
	}
	if cfg.APIKeys[0].APIKey != "legacy-key" || cfg.APIKeys[0].RequestsPerSecond != config.DefaultAPIKeyRequestsPerSecond {
		t.Fatalf("legacy key = %#v, want default rps", cfg.APIKeys[0])
	}
	if cfg.APIKeys[1].APIKey != "structured-key" || cfg.APIKeys[1].RequestsPerSecond != 9 {
		t.Fatalf("structured key = %#v, want rps 9", cfg.APIKeys[1])
	}
}

func TestPatchAPIKeys_LegacyStringValuePreservesExistingRPS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []sdkconfig.APIKeyEntry{{APIKey: "legacy-key", RequestsPerSecond: 11}},
		},
	}
	h := newAPIKeysTestHandler(t, cfg)

	r := gin.New()
	r.PATCH("/v0/management/api-keys", h.PatchAPIKeys)

	body := `{"index":0,"value":"updated-key"}`
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/api-keys", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH /api-keys status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if len(cfg.APIKeys) != 1 {
		t.Fatalf("len(cfg.APIKeys) = %d, want 1", len(cfg.APIKeys))
	}
	if cfg.APIKeys[0].APIKey != "updated-key" {
		t.Fatalf("updated key = %q, want updated-key", cfg.APIKeys[0].APIKey)
	}
	if cfg.APIKeys[0].RequestsPerSecond != 11 {
		t.Fatalf("updated rps = %d, want 11", cfg.APIKeys[0].RequestsPerSecond)
	}
}
