package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// TestConfigV8ManagesModelFallback covers the management contract used by the
// desktop UI: read, replace, validate, and delete routing.model-fallback.
func TestConfigV8ManagesModelFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("config-version: 8\nrouting:\n  strategy: round-robin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	reloads := make(chan *config.Config, 8)
	h.SetConfigReloadHook(func(_ context.Context, cfg *config.Config) { reloads <- cfg })
	router := gin.New()
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		router.Handle(method, "/v8/management/config/*path", h.ConfigV8)
	}
	request := func(method, url, body string, status int) string {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(method, url, strings.NewReader(body)))
		if recorder.Code != status {
			t.Fatalf("%s %s: status=%d body=%s", method, url, recorder.Code, recorder.Body.String())
		}
		return recorder.Body.String()
	}
	const endpoint = "/v8/management/config/routing/model-fallback"
	request(http.MethodGet, endpoint, "", http.StatusNotFound)

	request(http.MethodPut, endpoint, `{"enabled":true,"max-hops":2,"on":[429,"quota_exhausted"],"chains":{"gpt-6-astra":["claude-opus-5-5"]}}`, http.StatusOK)
	select {
	case reloaded := <-reloads:
		want := config.ModelFallbackConfig{Enabled: true, MaxHops: 2, On: []string{"429", "quota_exhausted"}, Chains: map[string][]string{"gpt-6-astra": {"claude-opus-5-5"}}}
		if !reflect.DeepEqual(reloaded.Routing.ModelFallback, want) {
			t.Fatalf("reloaded ModelFallback = %+v, want %+v", reloaded.Routing.ModelFallback, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("management write did not trigger a config reload")
	}
	var got map[string]any
	if err = json.Unmarshal([]byte(request(http.MethodGet, endpoint, "", http.StatusOK)), &got); err != nil {
		t.Fatal(err)
	}
	if got["enabled"] != true || got["max-hops"] != float64(2) {
		t.Fatalf("GET model-fallback = %v", got)
	}

	request(http.MethodPut, endpoint+"/enabled", `false`, http.StatusOK)
	request(http.MethodPut, endpoint+"/on", `[401]`, http.StatusBadRequest)
	request(http.MethodPut, endpoint+"/chains", `{"a":["a"]}`, http.StatusBadRequest)
	saved, _ := os.ReadFile(path)
	if strings.Contains(string(saved), "401") || !strings.Contains(string(saved), "enabled: false") {
		t.Fatalf("rejected writes changed the file:\n%s", saved)
	}
	request(http.MethodDelete, endpoint, "", http.StatusOK)
	request(http.MethodGet, endpoint, "", http.StatusNotFound)
}
