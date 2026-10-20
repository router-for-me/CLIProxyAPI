package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// TestConfigV8ETagExposedForCORS covers issue #6666 through the real server:
// a cross-origin management page must be able to read the ETag that the v8
// config endpoints return, otherwise the GET -> If-Match flow cannot run.
func TestConfigV8ETagExposedForCORS(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")

	server := newTestServer(t)
	if err := os.WriteFile(server.configFilePath, []byte("config-version: 8\nserver: {port: 8317}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := proxyconfig.LoadConfig(server.configFilePath)
	if err != nil {
		t.Fatal(err)
	}
	server.cfgMu.Lock()
	server.cfg = cfg
	server.cfgMu.Unlock()
	server.mgmt.SetConfig(cfg)

	do := func(method, body, ifMatch string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/v8/management/config.yaml", strings.NewReader(body))
		req.Header.Set("Origin", "http://127.0.0.1:5173")
		req.Header.Set("Authorization", "Bearer test-management-key")
		if ifMatch != "" {
			req.Header.Set("If-Match", ifMatch)
		}
		recorder := httptest.NewRecorder()
		server.engine.ServeHTTP(recorder, req)
		return recorder
	}
	wantETagExposed := func(recorder *httptest.ResponseRecorder, status int) string {
		t.Helper()
		if recorder.Code != status {
			t.Fatalf("status = %d, want %d body=%s", recorder.Code, status, recorder.Body.String())
		}
		exposed := false
		for _, name := range strings.Split(recorder.Header().Get("Access-Control-Expose-Headers"), ",") {
			if strings.EqualFold(strings.TrimSpace(name), "ETag") {
				exposed = true
			}
		}
		if !exposed {
			t.Fatalf("Access-Control-Expose-Headers missing ETag: %q", recorder.Header().Get("Access-Control-Expose-Headers"))
		}
		etag := recorder.Header().Get("ETag")
		if etag == "" {
			t.Fatal("response is missing ETag")
		}
		return etag
	}

	etag := wantETagExposed(do(http.MethodGet, "", ""), http.StatusOK)
	// A stale precondition reports the current version, still CORS-readable.
	wantETagExposed(do(http.MethodPut, "config-version: 8\n", `"stale"`), http.StatusPreconditionFailed)
	// A successful conditional write chains through its exposed new ETag.
	wantETagExposed(do(http.MethodPut, "config-version: 8\nserver: {port: 8318}\n", etag), http.StatusOK)
}
