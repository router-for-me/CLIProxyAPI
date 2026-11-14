package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestConfigV8NormalizesClientAPIKeysPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := "config-version: 8\nserver: {port: 8317}\naccess: {api-keys: [cpa-initial]}\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	h := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	router.PUT("/v8/management/config/*path", h.ConfigV8)
	router.GET("/v8/management/config/*path", h.ConfigV8)

	// PUT with raw keys lacking "cpa-" prefix and containing duplicates
	putBody := `["unprefixed-key", "cpa-already-prefixed", "unprefixed-key"]`
	req := httptest.NewRequest(http.MethodPut, "/v8/management/config/access/api-keys", strings.NewReader(putBody))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /v8/management/config/access/api-keys: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Verify via GET
	getReq := httptest.NewRequest(http.MethodGet, "/v8/management/config/access/api-keys", nil)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("GET /v8/management/config/access/api-keys: status=%d body=%s", getRec.Code, getRec.Body.String())
	}

	var keys []string
	if err := json.Unmarshal(getRec.Body.Bytes(), &keys); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}

	wantKeys := []string{"cpa-unprefixed-key", "cpa-already-prefixed"}
	if len(keys) != len(wantKeys) {
		t.Fatalf("got keys %#v, want %#v", keys, wantKeys)
	}
	for i, want := range wantKeys {
		if keys[i] != want {
			t.Errorf("keys[%d] = %q, want %q", i, keys[i], want)
		}
	}

	// Verify on-disk file
	reloaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if len(reloaded.APIKeys) != len(wantKeys) {
		t.Fatalf("reloaded APIKeys = %#v, want %#v", reloaded.APIKeys, wantKeys)
	}
}
