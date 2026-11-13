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

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// newConfigV8RevisionHarness starts a handler the way the running server does:
// the startup configuration is file-backed, so its source hash names the first
// applied revision, and its host, port, and TLS describe the listener the
// process serves for its whole lifetime.
func newConfigV8RevisionHarness(t *testing.T, raw string) (*Handler, *gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	h := NewHandler(cfg, path, nil)
	h.SetConfigReloadHook(func(context.Context, *config.Config) {})
	router := gin.New()
	router.PATCH("/v8/management/config", h.ConfigV8)
	router.PUT("/v8/management/config/*path", h.ConfigV8)
	return h, router, path
}

func doConfigV8Save(t *testing.T, router *gin.Engine, method, url, body string) map[string]any {
	t.Helper()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(method, url, strings.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("%s %s: status=%d body=%s", method, url, recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &response); errDecode != nil {
		t.Fatalf("%s %s: decode response: %v", method, url, errDecode)
	}
	return response
}

func requireStringField(t *testing.T, response map[string]any, key string) string {
	t.Helper()
	value, ok := response[key].(string)
	if !ok || value == "" {
		t.Fatalf("response field %s missing or empty: %v", key, response)
	}
	return value
}

// publishReload mirrors what the post-save watcher reload does in a running
// process: it loads the saved file and publishes it through SetConfig, which
// advances the applied revision.
func publishReload(t *testing.T, h *Handler, path string) {
	t.Helper()
	published, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	h.SetConfig(published)
}

// TestConfigV8SavePortRequiresRestart_Issue7461 replays the issue #7461
// reproduction: a management save changes the port, the save is acknowledged,
// but the process keeps listening on the port fixed at startup. The response
// must separate the saved revision from the applied one and must not present
// the change as fully effective.
func TestConfigV8SavePortRequiresRestart_Issue7461(t *testing.T) {
	h, router, path := newConfigV8RevisionHarness(t, "config-version: 8\nserver:\n  port: 8317\n")
	startupRevision := h.appliedSourceHash
	if startupRevision == "" {
		t.Fatal("startup config must carry a source hash naming the applied revision")
	}

	response := doConfigV8Save(t, router, http.MethodPut, "/v8/management/config/server/port", `8318`)

	savedBytes, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	savedRevision := config.ComputeSourceHash(savedBytes)
	if got := requireStringField(t, response, "saved-revision"); got != savedRevision {
		t.Fatalf("saved-revision = %q, want the hash of the bytes on disk %q", got, savedRevision)
	}
	if got := requireStringField(t, response, "applied-revision"); got != startupRevision {
		t.Fatalf("applied-revision = %q, want the startup revision %q", got, startupRevision)
	}
	if savedRevision == startupRevision {
		t.Fatal("saving a different port must produce a new saved revision")
	}
	if fields, ok := response["restart-required"].([]any); !ok || !reflect.DeepEqual(fields, []any{"server.port"}) {
		t.Fatalf("restart-required = %v, want [server.port]", response["restart-required"])
	}
	if response["status"] != "ok" || response["config-version"] != float64(8) {
		t.Fatalf("unexpected success envelope: %v", response)
	}

	// The post-save reload publishes the saved file, so the applied revision
	// catches up with the saved one. The listener still serves the startup
	// port, so the field must stay flagged as restart-required.
	publishReload(t, h, path)
	repeat := doConfigV8Save(t, router, http.MethodPut, "/v8/management/config/server/port", `8318`)
	if saved := requireStringField(t, repeat, "saved-revision"); saved != savedRevision {
		t.Fatalf("identical rewrite changed the saved revision: %q != %q", saved, savedRevision)
	}
	if got := requireStringField(t, repeat, "applied-revision"); got != savedRevision {
		t.Fatalf("applied-revision = %q after the reload published the save, want %q", got, savedRevision)
	}
	if fields, ok := repeat["restart-required"].([]any); !ok || !reflect.DeepEqual(fields, []any{"server.port"}) {
		t.Fatalf("restart-required = %v after reload, still want [server.port]", repeat["restart-required"])
	}

	// Changing the port back to the served value removes the flag: nothing is
	// left that a restart would apply.
	reverted := doConfigV8Save(t, router, http.MethodPut, "/v8/management/config/server/port", `8317`)
	if _, ok := reverted["restart-required"]; ok {
		t.Fatalf("restart-required = %v after restoring the served port, want absent", reverted["restart-required"])
	}
}

// TestConfigV8SaveHostAndTLSRequireRestart_Issue7461 checks the remaining
// listener and transport security fields: a save changing the bind host or the
// TLS material reports each changed field.
func TestConfigV8SaveHostAndTLSRequireRestart_Issue7461(t *testing.T) {
	_, router, _ := newConfigV8RevisionHarness(t, "config-version: 8\nserver:\n  port: 8317\n  tls:\n    enable: false\n")

	response := doConfigV8Save(t, router, http.MethodPatch, "/v8/management/config",
		`{"server":{"host":"127.0.0.1","tls":{"enable":true,"cert":"server.crt","key":"server.key"}}}`)

	want := []any{"server.host", "server.tls.enable", "server.tls.cert", "server.tls.key"}
	if fields, ok := response["restart-required"].([]any); !ok || !reflect.DeepEqual(fields, want) {
		t.Fatalf("restart-required = %v, want %v", response["restart-required"], want)
	}
	if _, ok := response["saved-revision"].(string); !ok {
		t.Fatal("host/TLS save response must still carry the saved revision")
	}
}

// TestConfigV8SaveWithoutListenerChangeNeedsNoRestart verifies that a save
// which does not touch the listener fields neither reports restart-required
// nor hides the pending reload: before the reload publishes, the saved and
// applied revisions differ; after it publishes, they match.
func TestConfigV8SaveWithoutListenerChangeNeedsNoRestart(t *testing.T) {
	h, router, path := newConfigV8RevisionHarness(t, "config-version: 8\nrouting:\n  retry:\n    request-retry: 3\n")
	startupRevision := h.appliedSourceHash

	pending := doConfigV8Save(t, router, http.MethodPut, "/v8/management/config/routing/retry/request-retry", `5`)
	if _, ok := pending["restart-required"]; ok {
		t.Fatalf("restart-required = %v for a non-listener save, want absent", pending["restart-required"])
	}
	savedRevision := requireStringField(t, pending, "saved-revision")
	if got := requireStringField(t, pending, "applied-revision"); got != startupRevision || got == savedRevision {
		t.Fatalf("before the reload publishes: applied-revision = %q, want the startup revision %q distinct from saved %q", got, startupRevision, savedRevision)
	}

	publishReload(t, h, path)
	applied := doConfigV8Save(t, router, http.MethodPut, "/v8/management/config/routing/retry/request-retry", `5`)
	if _, ok := applied["restart-required"]; ok {
		t.Fatalf("restart-required = %v for a non-listener save, want absent", applied["restart-required"])
	}
	if got := requireStringField(t, applied, "applied-revision"); got != savedRevision {
		t.Fatalf("applied-revision = %q after publication, want the saved revision %q", got, savedRevision)
	}
	if got := requireStringField(t, applied, "saved-revision"); got != savedRevision {
		t.Fatalf("identical rewrite changed the saved revision: %q != %q", got, savedRevision)
	}
}
