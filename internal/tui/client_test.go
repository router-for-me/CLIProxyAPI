package tui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestNewClientWithBaseURL(t *testing.T) {
	tests := []struct {
		name        string
		inputURL    string
		expectedURL string
	}{
		{
			name:        "standard http url",
			inputURL:    "http://192.168.1.100:8317",
			expectedURL: "http://192.168.1.100:8317",
		},
		{
			name:        "https url with trailing slash",
			inputURL:    "https://proxy.example.com/",
			expectedURL: "https://proxy.example.com",
		},
		{
			name:        "uppercase https url with trailing slash",
			inputURL:    "HTTPS://proxy.example.com/",
			expectedURL: "HTTPS://proxy.example.com",
		},
		{
			name:        "url without scheme",
			inputURL:    "proxy.example.com:9000",
			expectedURL: "http://proxy.example.com:9000",
		},
		{
			name:        "url with subpath and trailing slash",
			inputURL:    "https://proxy.example.com/prefix/",
			expectedURL: "https://proxy.example.com/prefix",
		},
		{
			name:        "empty url falls back to default localhost",
			inputURL:    "",
			expectedURL: "http://127.0.0.1:8317",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := NewClientWithBaseURL(tc.inputURL, "secret")
			if client.baseURL != tc.expectedURL {
				t.Fatalf("expected baseURL %q, got %q", tc.expectedURL, client.baseURL)
			}
		})
	}
}

func TestNewClient_BackwardsCompatibility(t *testing.T) {
	client := NewClient(8317, "test-secret")
	expected := "http://127.0.0.1:8317"
	if client.baseURL != expected {
		t.Fatalf("expected baseURL %q, got %q", expected, client.baseURL)
	}
}

func TestClient_RemoteServerInteraction(t *testing.T) {
	var requestedPath string
	var authHeader string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		authHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server": {"port": 8317}, "observability": {"logs": {"debug": true}}}`))
	}))
	defer ts.Close()

	client := NewClientWithBaseURL(ts.URL, "remote-secret-key")
	cfg, err := client.GetConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requestedPath != "/v8/management/config" {
		t.Fatalf("expected path /v8/management/config, got %q", requestedPath)
	}
	if authHeader != "Bearer remote-secret-key" {
		t.Fatalf("expected Authorization Bearer remote-secret-key, got %q", authHeader)
	}
	if getFloat(cfg, "port") != 8317 || !getBool(cfg, "debug") {
		t.Fatalf("expected projected legacy config, got %v", cfg)
	}
}

func TestNewAppWithBaseURL(t *testing.T) {
	app := NewAppWithBaseURL("https://proxy.example.com", "secret", nil)
	if app.client.baseURL != "https://proxy.example.com" {
		t.Fatalf("expected app client baseURL https://proxy.example.com, got %q", app.client.baseURL)
	}
}

func TestClient_GetAuthFilesUsesV8CredentialsAndRendersRecoveryState(t *testing.T) {
	var requestedPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"files": []map[string]any{
				{
					"name":                    "codex-a.json",
					"provider":                "codex",
					"email":                   "a@example.com",
					"status":                  "active",
					"disabled":                false,
					"unavailable":             false,
					"requires_sign_in":        true,
					"renewal_state":           "sign_in_required",
					"recovery_stage":          "sign_in_required",
					"access_token_expires_at": "2026-10-31T00:00:00Z",
					"next_action":             "sign_in",
					"recovery_hint":           "Re-authenticate this account on this proxy instance via OAuth login.",
				},
			},
		})
	}))
	defer ts.Close()

	client := NewClientWithBaseURL(ts.URL, "secret")
	files, err := client.GetAuthFiles()
	if err != nil {
		t.Fatalf("GetAuthFiles error: %v", err)
	}
	if requestedPath != "/v8/management/credentials" {
		t.Fatalf("requestedPath = %q, want /v8/management/credentials", requestedPath)
	}
	if len(files) != 1 {
		t.Fatalf("len(files) = %d, want 1", len(files))
	}

	tab := newAuthTabModel(client)
	tab.SetSize(120, 40)
	tab.files = files
	tab.expanded = 0
	view := tab.renderContent()
	for _, expected := range []string{
		"sign-in required",
		"sign_in_required",
		"2026-10-31T00:00:00Z",
		"sign_in",
		"Re-authenticate this account on this proxy instance via OAuth login.",
	} {
		if !containsString(view, expected) {
			t.Fatalf("expected TUI auth tab view to contain %q, got:\n%s", expected, view)
		}
	}
}

func containsString(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && (haystack == needle || indexString(haystack, needle) >= 0))
}

// capturedRequest records one interaction with the fake management server.
type capturedRequest struct {
	method      string
	path        string
	query       string
	body        string
	ifMatch     string
	contentType string
}

// fakeManagementServer replays the response shapes produced by the current
// and deprecated management handlers in this repository. v8Routes and
// v0Routes toggle which route generations the fake server serves; routes
// outside the enabled sets answer 404 like an unregistered gin route.
type fakeManagementServer struct {
	t        *testing.T
	v8Routes map[string]bool
	v0Routes map[string]bool
	// v8ConfigETag is the ETag advertised by v8 config reads; v8WriteETag is
	// returned by v8 config writes, mirroring the ConfigV8 handler contract.
	v8ConfigETag string
	v8WriteETag  string
	// v8Tree is served by GET /v8/management/config.
	v8Tree string
	// v8YAML is served by GET /v8/management/config.yaml.
	v8YAML string
	// v0Config is served by GET /v0/management/config.
	v0Config string

	mu        sync.Mutex
	requests  []capturedRequest
	pretag412 bool
}

func newFakeManagementServer(t *testing.T) *fakeManagementServer {
	t.Helper()
	return &fakeManagementServer{
		t: t,
		v8Routes: map[string]bool{
			"GET /v8/management/config":               true,
			"GET /v8/management/config.yaml":          true,
			"PUT /v8/management/config.yaml":          true,
			"PUT /v8/management/config/*":             true,
			"DELETE /v8/management/config/*":          true,
			"DELETE /v8/management/credentials":       true,
			"PATCH /v8/management/credentials/status": true,
			"PATCH /v8/management/credentials/fields": true,
			"POST /v8/management/credentials/refresh": true,
		},
		v0Routes: map[string]bool{
			"GET /v0/management/config":              true,
			"GET /v0/management/config.yaml":         true,
			"PUT /v0/management/config.yaml":         true,
			"PUT /v0/management/*":                   true,
			"DELETE /v0/management/auth-files":       true,
			"PATCH /v0/management/auth-files/status": true,
			"PATCH /v0/management/auth-files/fields": true,
			"POST /v0/management/auth-files/refresh": true,
		},
		// Shapes below mirror the current handlers: the v8 tree is what
		// ConfigV8 decodes from a stored config-version 8 document, and the
		// flat body matches the legacy GetConfig runtime JSON.
		v8ConfigETag: `"v8-config-tag"`,
		v8WriteETag:  `"v8-saved-tag"`,
		v8Tree: `{
  "config-version": 8,
  "server": {"host": "127.0.0.1", "port": 8317},
  "routing": {"strategy": "round-robin", "force-model-prefix": false, "retry": {"request-retry": 4, "max-retry-credentials": 0, "max-retry-interval": 30}},
  "requests": {"proxy-url": "socks5://127.0.0.1:1080"},
  "observability": {"logs": {"debug": true, "logging-to-file": false, "logs-max-total-size-mb": 100, "error-logs-max-files": 10, "request-log": false}, "usage": {"usage-statistics-enabled": true}},
  "oauth": {"providers": {"aistudio": {"ws-auth": false}}},
  "quota-exceeded": {"switch-project": true, "switch-preview-model": true}
}`,
		v8YAML:   "config-version: 8\nserver: {port: 8317}\n",
		v0Config: `{"debug": true, "request-retry": 4, "proxy-url": "socks5://127.0.0.1:1080", "routing": {"strategy": "round-robin"}, "quota-exceeded": {"switch-project": true}, "ws-auth": true}`,
	}
}

func (f *fakeManagementServer) serve(w http.ResponseWriter, r *http.Request) {
	body, errRead := io.ReadAll(r.Body)
	if errRead != nil {
		f.t.Fatalf("read request body: %v", errRead)
	}
	f.mu.Lock()
	f.requests = append(f.requests, capturedRequest{
		method:      r.Method,
		path:        r.URL.Path,
		query:       r.URL.RawQuery,
		body:        string(body),
		ifMatch:     r.Header.Get("If-Match"),
		contentType: r.Header.Get("Content-Type"),
	})
	pretags := f.pretag412
	f.mu.Unlock()

	route := r.Method + " " + r.URL.Path
	routeEnabled := func() bool {
		if f.v8Routes[route] || f.v0Routes[route] {
			return true
		}
		switch {
		case strings.HasPrefix(route, "PUT /v8/management/config/"):
			return f.v8Routes["PUT /v8/management/config/*"]
		case strings.HasPrefix(route, "DELETE /v8/management/config/"):
			return f.v8Routes["DELETE /v8/management/config/*"]
		case strings.HasPrefix(route, "PUT /v0/management/"):
			return f.v0Routes["PUT /v0/management/*"]
		}
		return false
	}
	if !routeEnabled() {
		// Unregistered gin routes answer with a plain 404 page.
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("404 page not found"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch route {
	case "GET /v8/management/config":
		w.Header().Set("ETag", f.v8ConfigETag)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(f.v8Tree))
	case "GET /v8/management/config.yaml":
		w.Header().Set("ETag", f.v8ConfigETag)
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(f.v8YAML))
	case "PUT /v8/management/config.yaml":
		if pretags {
			w.Header().Set("ETag", f.v8ConfigETag)
			w.WriteHeader(http.StatusPreconditionFailed)
			_, _ = w.Write([]byte(`{"error": "precondition_failed"}`))
			return
		}
		w.Header().Set("ETag", f.v8WriteETag)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status": "ok", "config-version": 8}`))
	case "PATCH /v8/management/credentials/status":
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status": "ok", "disabled": true}`))
	case "DELETE /v8/management/credentials":
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status": "ok"}`))
	case "POST /v8/management/credentials/refresh":
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok": true, "auth": {"id": "codex-a.json"}}`))
	default:
		if strings.HasPrefix(r.URL.Path, "/v8/management/config/") {
			if pretags {
				w.Header().Set("ETag", f.v8ConfigETag)
				w.WriteHeader(http.StatusPreconditionFailed)
				_, _ = w.Write([]byte(`{"error": "precondition_failed"}`))
				return
			}
			w.Header().Set("ETag", f.v8WriteETag)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status": "ok", "config-version": 8}`))
			return
		}
		switch route {
		case "GET /v0/management/config.yaml":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("port: 8317\n"))
		case "PUT /v0/management/config.yaml":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok": true, "changed": ["config"]}`))
		case "GET /v0/management/config":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(f.v0Config))
		case "DELETE /v0/management/auth-files":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status": "ok"}`))
		case "PATCH /v0/management/auth-files/status":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status": "ok", "disabled": true}`))
		case "PATCH /v0/management/auth-files/fields":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status": "ok"}`))
		case "POST /v0/management/auth-files/refresh":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok": true, "auth": {"id": "codex-a.json"}}`))
		default:
			// Legacy field writes reply like Handler.persistLocked.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status": "ok"}`))
		}
	}
}

func (f *fakeManagementServer) start(t *testing.T) *Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(ts.Close)
	return NewClientWithBaseURL(ts.URL, "test-secret")
}

func (f *fakeManagementServer) recorded() []capturedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]capturedRequest(nil), f.requests...)
}

// TestClient_ConfigPrefersV8RoutesWithIfMatch replays a server that serves
// the current management contract: reads use /v8/management/config, writes
// carry the ETag collected by the latest read as an If-Match precondition,
// and field writes take the raw-value body shape of the v8 route.
func TestClient_ConfigPrefersV8RoutesWithIfMatch(t *testing.T) {
	fake := newFakeManagementServer(t)
	client := fake.start(t)

	cfg, err := client.GetConfig()
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if getFloat(cfg, "port") != 8317 || getString(cfg, "host") != "127.0.0.1" {
		t.Fatalf("port/host not projected from v8 tree: %v", cfg)
	}
	if !getBool(cfg, "debug") || getFloat(cfg, "request-retry") != 4 {
		t.Fatalf("flat settings not projected from v8 tree: %v", cfg)
	}
	if getString(cfg, "proxy-url") != "socks5://127.0.0.1:1080" {
		t.Fatalf("proxy-url not projected from v8 tree: %v", cfg)
	}
	if getBool(cfg, "ws-auth") {
		t.Fatalf("ws-auth not projected from v8 tree: %v", cfg)
	}
	if !getBoolNested(cfg, "quota-exceeded", "switch-project") {
		t.Fatalf("quota-exceeded not projected from v8 tree: %v", cfg)
	}
	if routing, ok := cfg["routing"].(map[string]any); !ok || getString(routing, "strategy") != "round-robin" {
		t.Fatalf("routing strategy not projected from v8 tree: %v", cfg)
	}

	if err = client.PutBoolField("debug", false); err != nil {
		t.Fatalf("PutBoolField: %v", err)
	}
	requests := fake.recorded()
	fieldWrite := requests[len(requests)-1]
	if fieldWrite.method != "PUT" || fieldWrite.path != "/v8/management/config/observability/logs/debug" {
		t.Fatalf("field write went to %s %s, want PUT /v8/management/config/observability/logs/debug", fieldWrite.method, fieldWrite.path)
	}
	if fieldWrite.ifMatch != `"v8-config-tag"` {
		t.Fatalf("field write If-Match = %q, want the ETag from the preceding read", fieldWrite.ifMatch)
	}
	if fieldWrite.body != "false" {
		t.Fatalf("field write body = %q, want raw JSON value %q", fieldWrite.body, "false")
	}

	if err = client.PutConfigYAML("config-version: 8\n"); err != nil {
		t.Fatalf("PutConfigYAML: %v", err)
	}
	requests = fake.recorded()
	yamlWrite := requests[len(requests)-1]
	if yamlWrite.method != "PUT" || yamlWrite.path != "/v8/management/config.yaml" {
		t.Fatalf("yaml write went to %s %s, want PUT /v8/management/config.yaml", yamlWrite.method, yamlWrite.path)
	}
	// The ETag of the successful field write must chain into the next write.
	if yamlWrite.ifMatch != `"v8-saved-tag"` {
		t.Fatalf("yaml write If-Match = %q, want the ETag returned by the previous write", yamlWrite.ifMatch)
	}
	if yamlWrite.contentType != "application/yaml" {
		t.Fatalf("yaml write Content-Type = %q, want application/yaml", yamlWrite.contentType)
	}

	if _, err = client.GetConfigYAML(); err != nil {
		t.Fatalf("GetConfigYAML: %v", err)
	}
	requests = fake.recorded()
	yamlRead := requests[len(requests)-1]
	if yamlRead.path != "/v8/management/config.yaml" {
		t.Fatalf("yaml read went to %s, want /v8/management/config.yaml", yamlRead.path)
	}
}

// TestClient_StalePreconditionSurfacesAsError verifies a 412 from the current
// route is reported instead of being silently treated as success.
func TestClient_StalePreconditionSurfacesAsError(t *testing.T) {
	fake := newFakeManagementServer(t)
	fake.pretag412 = true
	client := fake.start(t)

	if _, err := client.GetConfig(); err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	err := client.PutBoolField("debug", false)
	if err == nil || !containsString(err.Error(), "HTTP 412") {
		t.Fatalf("PutBoolField error = %v, want HTTP 412 precondition failure", err)
	}
	err = client.PutConfigYAML("config-version: 8\n")
	if err == nil || !containsString(err.Error(), "HTTP 412") {
		t.Fatalf("PutConfigYAML error = %v, want HTTP 412 precondition failure", err)
	}
}

// TestClient_FallsBackToLegacyRoutes replays a server that only serves the
// deprecated routes: every migrated operation must still work there.
func TestClient_FallsBackToLegacyRoutes(t *testing.T) {
	fake := newFakeManagementServer(t)
	for route := range fake.v8Routes {
		delete(fake.v8Routes, route)
	}
	client := fake.start(t)

	cfg, err := client.GetConfig()
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if !getBool(cfg, "debug") || getFloat(cfg, "request-retry") != 4 {
		t.Fatalf("legacy config not returned: %v", cfg)
	}

	if _, err = client.GetConfigYAML(); err != nil {
		t.Fatalf("GetConfigYAML: %v", err)
	}
	if err = client.PutConfigYAML("port: 8317\n"); err != nil {
		t.Fatalf("PutConfigYAML: %v", err)
	}
	if err = client.PutBoolField("debug", true); err != nil {
		t.Fatalf("PutBoolField: %v", err)
	}
	if err = client.PutIntField("request-retry", 2); err != nil {
		t.Fatalf("PutIntField: %v", err)
	}
	if err = client.PutStringField("proxy-url", "direct"); err != nil {
		t.Fatalf("PutStringField: %v", err)
	}
	if err = client.DeleteAuthFile("codex-a.json"); err != nil {
		t.Fatalf("DeleteAuthFile: %v", err)
	}
	if err = client.ToggleAuthFile("codex-a.json", true); err != nil {
		t.Fatalf("ToggleAuthFile: %v", err)
	}
	if err = client.PatchAuthFileFields("codex-a.json", map[string]any{"email": "a@example.com"}); err != nil {
		t.Fatalf("PatchAuthFileFields: %v", err)
	}
	if err = client.RefreshAuthFile("codex-a.json"); err != nil {
		t.Fatalf("RefreshAuthFile: %v", err)
	}
	if err = client.RefreshAllAuthFiles(); err != nil {
		t.Fatalf("RefreshAllAuthFiles: %v", err)
	}

	requests := fake.recorded()
	paths := make([]string, 0, len(requests))
	for _, request := range requests {
		paths = append(paths, request.method+" "+request.path)
	}
	want := []string{
		"GET /v8/management/config",
		"GET /v0/management/config",
		"GET /v8/management/config.yaml",
		"GET /v0/management/config.yaml",
		"PUT /v8/management/config.yaml",
		"PUT /v0/management/config.yaml",
		"PUT /v8/management/config/observability/logs/debug",
		"PUT /v0/management/debug",
		"PUT /v8/management/config/routing/retry/request-retry",
		"PUT /v0/management/request-retry",
		"PUT /v8/management/config/requests/proxy-url",
		"PUT /v0/management/proxy-url",
		"DELETE /v8/management/credentials",
		"DELETE /v0/management/auth-files",
		"PATCH /v8/management/credentials/status",
		"PATCH /v0/management/auth-files/status",
		"PATCH /v8/management/credentials/fields",
		"PATCH /v0/management/auth-files/fields",
		"POST /v8/management/credentials/refresh",
		"POST /v0/management/auth-files/refresh",
		"POST /v8/management/credentials/refresh",
		"POST /v0/management/auth-files/refresh",
	}
	if len(paths) != len(want) {
		t.Fatalf("request sequence = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("request %d = %q, want %q (full sequence %v)", i, paths[i], want[i], paths)
		}
	}

	// Legacy field writes keep the {"value": ...} body shape.
	for _, request := range requests {
		if request.path == "/v0/management/debug" {
			if request.body != `{"value":true}` {
				t.Fatalf("legacy debug write body = %q, want {\"value\":true}", request.body)
			}
			if request.ifMatch != "" {
				t.Fatalf("legacy write must not carry If-Match, got %q", request.ifMatch)
			}
		}
	}
}

// TestClient_ConfigRoutesUnavailableError replays a server that serves
// neither route generation: operations must fail with an explicit error.
func TestClient_ConfigRoutesUnavailableError(t *testing.T) {
	fake := newFakeManagementServer(t)
	fake.v8Routes = map[string]bool{}
	fake.v0Routes = map[string]bool{}
	client := fake.start(t)

	if _, err := client.GetConfig(); err == nil || !containsString(err.Error(), "management endpoint unavailable") {
		t.Fatalf("GetConfig error = %v, want explicit unavailable error", err)
	}
	if _, err := client.GetConfigYAML(); err == nil || !containsString(err.Error(), "management endpoint unavailable") {
		t.Fatalf("GetConfigYAML error = %v, want explicit unavailable error", err)
	}
	if err := client.PutConfigYAML("port: 8317\n"); err == nil || !containsString(err.Error(), "management endpoint unavailable") {
		t.Fatalf("PutConfigYAML error = %v, want explicit unavailable error", err)
	}
	if err := client.PutBoolField("debug", true); err == nil || !containsString(err.Error(), "management endpoint unavailable") {
		t.Fatalf("PutBoolField error = %v, want explicit unavailable error", err)
	}
}

// TestClient_CredentialsMutationsPreferV8Routes verifies delete, status,
// fields, and refresh reach the current credentials routes first and succeed
// without touching the deprecated ones.
func TestClient_CredentialsMutationsPreferV8Routes(t *testing.T) {
	fake := newFakeManagementServer(t)
	for route := range fake.v0Routes {
		delete(fake.v0Routes, route)
	}
	client := fake.start(t)

	if err := client.DeleteAuthFile("codex-a.json"); err != nil {
		t.Fatalf("DeleteAuthFile: %v", err)
	}
	if err := client.ToggleAuthFile("codex-a.json", true); err != nil {
		t.Fatalf("ToggleAuthFile: %v", err)
	}
	if err := client.PatchAuthFileFields("codex-a.json", map[string]any{"email": "a@example.com"}); err != nil {
		t.Fatalf("PatchAuthFileFields: %v", err)
	}
	if err := client.RefreshAuthFile("codex-a.json"); err != nil {
		t.Fatalf("RefreshAuthFile: %v", err)
	}
	if err := client.RefreshAllAuthFiles(); err != nil {
		t.Fatalf("RefreshAllAuthFiles: %v", err)
	}

	requests := fake.recorded()
	for _, request := range requests {
		if strings.Contains(request.path, "/v0/") {
			t.Fatalf("request hit deprecated route %s %s", request.method, request.path)
		}
	}
	last := requests[len(requests)-1]
	if last.path != "/v8/management/credentials/refresh" || !containsString(last.body, `"all":true`) {
		t.Fatalf("refresh-all request = %s %s %q", last.method, last.path, last.body)
	}
}

func indexString(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
