package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gin "github.com/gin-gonic/gin"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	internalusage "github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()

	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	cfg := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{"test-key"},
		},
		Port:                   0,
		AuthDir:                authDir,
		Debug:                  true,
		LoggingToFile:          false,
		UsageStatisticsEnabled: false,
		RemoteManagement:       proxyconfig.RemoteManagement{DisableControlPanel: true},
	}

	authManager := auth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()

	configPath := filepath.Join(tmpDir, "config.yaml")
	return NewServer(cfg, authManager, accessManager, configPath)
}

func TestInjectManagementConfigVersionGuard(t *testing.T) {
	html := []byte("<html><body><main>ok</main></body></html>")

	out := injectManagementConfigVersionGuard(html, "sha256:test-version")

	if !strings.Contains(string(out), "cliproxy-config-version-guard") {
		t.Fatalf("expected injected guard script, got %s", string(out))
	}
	if !strings.Contains(string(out), `var latestConfigVersion = "sha256:test-version"`) {
		t.Fatalf("expected initial config version in guard script: %s", string(out))
	}
	if strings.Index(string(out), "cliproxy-config-version-guard") > strings.Index(string(out), "</body>") {
		t.Fatalf("guard script should be injected before closing body: %s", string(out))
	}
	again := injectManagementConfigVersionGuard(out, "sha256:test-version")
	if string(again) != string(out) {
		t.Fatalf("guard injection should be idempotent")
	}
}

func TestInjectManagementConfigVersionGuardReplacesOldGuard(t *testing.T) {
	html := []byte(`<html><body><script id="cliproxy-config-version-guard">old guard</script><main>ok</main></body></html>`)

	out := injectManagementConfigVersionGuard(html)
	body := string(out)

	if strings.Contains(body, "old guard") {
		t.Fatalf("expected old guard script to be replaced: %s", body)
	}
	if strings.Count(body, "cliproxy-config-version-guard") != 1 {
		t.Fatalf("expected exactly one guard script, got %s", body)
	}
	if !strings.Contains(body, "writeQueue") {
		t.Fatalf("expected serialized write queue in guard script: %s", body)
	}
	if !strings.Contains(body, "cliproxy:config-conflict") {
		t.Fatalf("expected conflict event publishing in guard script: %s", body)
	}
	if !strings.Contains(body, "response && response.status === 409") {
		t.Fatalf("expected guard to avoid promoting conflict versions to writable versions: %s", body)
	}
	if !strings.Contains(body, "detachAPICallAbortSignal") {
		t.Fatalf("expected api-call abort signal detachment in guard script: %s", body)
	}
	if !strings.Contains(body, `path === "/v0/management/api-call"`) {
		t.Fatalf("expected api-call queue detection in guard script: %s", body)
	}
	if !strings.Contains(body, "__cliproxySequentialMap") {
		t.Fatalf("expected sequential key test helper in guard script: %s", body)
	}
}

func TestInjectManagementConfigVersionGuardSerializesKeyTestBatches(t *testing.T) {
	html := []byte(`<html><body><script type="module">
let a=(await Promise.all(t.map(e=>I(e)))).filter(Boolean).length;
let b=(await Promise.all(t.map(e=>we(e)))).filter(Boolean).length;
let c=(await Promise.all(t.map(e=>P(e)))).filter(Boolean).length;
let d=(await Promise.all(t.map(e=>De(e)))).filter(Boolean).length;
let f=(await Promise.all(t.map(e=>N(e)))).filter(Boolean).length;
</script></body></html>`)

	out := injectManagementConfigVersionGuard(html)
	body := string(out)

	for _, fragment := range []string{
		`Promise.all(t.map(e=>I(e)))`,
		`Promise.all(t.map(e=>we(e)))`,
		`Promise.all(t.map(e=>P(e)))`,
		`Promise.all(t.map(e=>De(e)))`,
		`Promise.all(t.map(e=>N(e)))`,
	} {
		if strings.Contains(body, fragment) {
			t.Fatalf("expected parallel key test fragment %q to be patched: %s", fragment, body)
		}
	}
	for _, fragment := range []string{
		`window.__cliproxySequentialMap(t,e=>I(e))`,
		`window.__cliproxySequentialMap(t,e=>we(e))`,
		`window.__cliproxySequentialMap(t,e=>P(e))`,
		`window.__cliproxySequentialMap(t,e=>De(e))`,
		`window.__cliproxySequentialMap(t,e=>N(e))`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("expected sequential key test fragment %q in patched body: %s", fragment, body)
		}
	}
}

func TestInjectManagementConfigVersionGuardKeepsDisabledKeysInSameGroup(t *testing.T) {
	html := []byte(`<html><body><script type="module">
const jg=(e,t)=>JSON.stringify({provider:e,baseUrl:Tg(e,t.baseUrl),excludedModels:kg(t.excludedModels),cloak:e==="claude"?Ag(t.cloak):null});
</script></body></html>`)

	out := injectManagementConfigVersionGuard(html)
	body := string(out)

	if strings.Contains(body, `excludedModels:kg(t.excludedModels)`) {
		t.Fatalf("expected disabled-key grouping signature to be patched: %s", body)
	}
	if !strings.Contains(body, `excludedModels:kg(Lp(t.excludedModels))`) {
		t.Fatalf("expected grouping signature to ignore disable-all sentinel: %s", body)
	}

	again := injectManagementConfigVersionGuard(out)
	if strings.Count(string(again), `excludedModels:kg(Lp(t.excludedModels))`) != 1 {
		t.Fatalf("expected grouping patch to be idempotent: %s", string(again))
	}
}

func TestInjectManagementConfigVersionGuardPatchesAuthIndexStats(t *testing.T) {
	html := []byte(`<html><body><script type="module">` +
		string(managementPanelAPIKeyAuthIndexNeedle) +
		string(managementPanelOpenAIKeyAuthIndexNeedle) +
		string(managementPanelOpenAIProviderAuthIndexNeedle) +
		string(managementPanelAIProviderStatsNeedle) +
		string(managementPanelAIProviderStatusBlocksNeedle) +
		string(managementPanelGroupedProviderStatsNeedle) +
		string(managementPanelOpenAIProviderStatsNeedle) +
		`</script></body></html>`)

	out := injectManagementConfigVersionGuard(html)
	body := string(out)

	for _, oldFragment := range []string{
		string(managementPanelAPIKeyAuthIndexNeedle),
		string(managementPanelOpenAIKeyAuthIndexNeedle),
		string(managementPanelOpenAIProviderAuthIndexNeedle),
		string(managementPanelAIProviderStatsNeedle),
		string(managementPanelAIProviderStatusBlocksNeedle),
		string(managementPanelGroupedProviderStatsNeedle),
		string(managementPanelOpenAIProviderStatsNeedle),
	} {
		if strings.Contains(body, oldFragment) {
			t.Fatalf("expected auth-index stats fragment to be patched, still found %q in %s", oldFragment, body)
		}
	}
	for _, expected := range []string{
		"i.authIndex=h",
		"authIndex:Ud",
		"t.byAuthIndex",
		"o.set(e,$p(t.blocks",
		"wm(n.apiKey,t,n.prefix,n.authIndex??n.auth_index)",
		"e?.authIndex??e?.auth_index",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected patched auth-index stats fragment %q in %s", expected, body)
		}
	}
}

func TestUsagePersistenceEnabledHotReload(t *testing.T) {
	t.Setenv("PGSTORE_DSN", "")
	t.Setenv("pgstore_dsn", "")
	t.Setenv("PGSTORE_SCHEMA", "")
	t.Setenv("pgstore_schema", "")

	internalusage.CloseDatabasePlugin()
	defer internalusage.CloseDatabasePlugin()

	server := newTestServer(t)

	disabled := *server.cfg
	disabled.UsagePersistenceEnabled = false
	server.UpdateClients(&disabled)
	if internalusage.GetDatabasePlugin() != nil {
		t.Fatalf("expected database plugin to be nil when disabled")
	}

	enabled := disabled
	enabled.UsagePersistenceEnabled = true
	server.UpdateClients(&enabled)
	firstPlugin := internalusage.GetDatabasePlugin()
	if firstPlugin == nil {
		t.Fatalf("expected database plugin to be initialized when enabled")
	}
	if _, err := os.Stat(filepath.Join(enabled.AuthDir, "usage.db")); err != nil {
		t.Fatalf("expected sqlite usage db to exist: %v", err)
	}

	disabledAgain := enabled
	disabledAgain.UsagePersistenceEnabled = false
	server.UpdateClients(&disabledAgain)
	if internalusage.GetDatabasePlugin() != nil {
		t.Fatalf("expected database plugin to be nil after disabling")
	}

	enabledAgain := disabledAgain
	enabledAgain.UsagePersistenceEnabled = true
	server.UpdateClients(&enabledAgain)
	secondPlugin := internalusage.GetDatabasePlugin()
	if secondPlugin == nil {
		t.Fatalf("expected database plugin to be initialized after re-enabling")
	}
	if secondPlugin == firstPlugin {
		t.Fatalf("expected database plugin to be re-initialized after re-enabling")
	}
}

func TestHealthz(t *testing.T) {
	server := newTestServer(t)

	t.Run("GET", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}

		var resp struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response JSON: %v; body=%s", err, rr.Body.String())
		}
		if resp.Status != "ok" {
			t.Fatalf("unexpected response status: got %q want %q", resp.Status, "ok")
		}
	})

	t.Run("HEAD", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodHead, "/healthz", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
		if rr.Body.Len() != 0 {
			t.Fatalf("expected empty body for HEAD request, got %q", rr.Body.String())
		}
	})
}

func TestUnsupportedOpenAIAudioEndpoints(t *testing.T) {
	server := newTestServer(t)
	paths := []string{
		"/v1/audio/transcriptions",
		"/v1/audio/translations",
		"/v1/audio/speech",
	}

	for _, path := range paths {
		path := path
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"gpt-5.5"}`))
			req.Header.Set("Authorization", "Bearer test-key")

			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Fatalf("unexpected status code for %s: got %d want %d; body=%s", path, rr.Code, http.StatusBadRequest, rr.Body.String())
			}

			var resp struct {
				Error struct {
					Code string `json:"code"`
					Type string `json:"type"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to parse response JSON: %v; body=%s", err, rr.Body.String())
			}
			if resp.Error.Code != "unsupported_endpoint" {
				t.Fatalf("unexpected error code for %s: got %q", path, resp.Error.Code)
			}
			if resp.Error.Type != "invalid_request_error" {
				t.Fatalf("unexpected error type for %s: got %q", path, resp.Error.Type)
			}
		})
	}
}

func TestUnifiedModelsHandlerRoutesClaudeCompatibleClients(t *testing.T) {
	testCases := []struct {
		name         string
		userAgent    string
		anthropicVer string
		apiKeyHeader bool
		wantClaude   bool
	}{
		{
			name:       "claude cli",
			userAgent:  "claude-cli/2.1.70 (external, cli)",
			wantClaude: true,
		},
		{
			name:       "anthropic js sdk",
			userAgent:  "Anthropic/JS 0.91.1",
			wantClaude: true,
		},
		{
			name:         "anthropic version header",
			userAgent:    "node",
			anthropicVer: "2023-06-01",
			wantClaude:   true,
		},
		{
			name:         "x api key header",
			userAgent:    "node",
			apiKeyHeader: true,
			wantClaude:   true,
		},
		{
			name:       "openai client",
			userAgent:  "curl/8.7.1",
			wantClaude: false,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)

			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			if tc.apiKeyHeader {
				req.Header.Set("X-Api-Key", "test-key")
			} else {
				req.Header.Set("Authorization", "Bearer test-key")
			}
			req.Header.Set("User-Agent", tc.userAgent)
			if tc.anthropicVer != "" {
				req.Header.Set("Anthropic-Version", tc.anthropicVer)
			}

			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
			}

			var resp map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to parse response JSON: %v; body=%s", err, rr.Body.String())
			}
			_, hasMore := resp["has_more"]
			_, hasObject := resp["object"]
			if tc.wantClaude && (!hasMore || hasObject) {
				t.Fatalf("expected Claude models response, got %s", rr.Body.String())
			}
			if !tc.wantClaude && (!hasObject || hasMore) {
				t.Fatalf("expected OpenAI models response, got %s", rr.Body.String())
			}
		})
	}
}

func TestManagementUsageRequiresManagementAuthAndPopsArray(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")

	prevQueueEnabled := redisqueue.Enabled()
	redisqueue.SetEnabled(false)
	t.Cleanup(func() {
		redisqueue.SetEnabled(false)
		redisqueue.SetEnabled(prevQueueEnabled)
	})

	server := newTestServer(t)

	redisqueue.Enqueue([]byte(`{"id":1}`))
	redisqueue.Enqueue([]byte(`{"id":2}`))

	missingKeyReq := httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=2", nil)
	missingKeyRR := httptest.NewRecorder()
	server.engine.ServeHTTP(missingKeyRR, missingKeyReq)
	if missingKeyRR.Code != http.StatusUnauthorized {
		t.Fatalf("missing key status = %d, want %d body=%s", missingKeyRR.Code, http.StatusUnauthorized, missingKeyRR.Body.String())
	}

	// Fork-specific: /v0/management/usage is repurposed for GetUsageStatistics (not legacy 404).
	// The queue endpoint lives at /v0/management/usage-queue.

	authReq := httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=2", nil)
	authReq.Header.Set("Authorization", "Bearer test-management-key")
	authRR := httptest.NewRecorder()
	server.engine.ServeHTTP(authRR, authReq)
	if authRR.Code != http.StatusOK {
		t.Fatalf("authenticated status = %d, want %d body=%s", authRR.Code, http.StatusOK, authRR.Body.String())
	}

	var payload []json.RawMessage
	if errUnmarshal := json.Unmarshal(authRR.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v body=%s", errUnmarshal, authRR.Body.String())
	}
	if len(payload) != 2 {
		t.Fatalf("response records = %d, want 2", len(payload))
	}
	for i, raw := range payload {
		var record struct {
			ID int `json:"id"`
		}
		if errUnmarshal := json.Unmarshal(raw, &record); errUnmarshal != nil {
			t.Fatalf("unmarshal record %d: %v", i, errUnmarshal)
		}
		if record.ID != i+1 {
			t.Fatalf("record %d id = %d, want %d", i, record.ID, i+1)
		}
	}

	if remaining := redisqueue.PopOldest(1); len(remaining) != 0 {
		t.Fatalf("remaining queue = %q, want empty", remaining)
	}
}

func TestHomeEnabledHidesManagementEndpointsAndControlPanel(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")

	server := newTestServer(t)
	server.cfg.Home.Enabled = true

	t.Run("management endpoints return 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v0/management/config", nil)
		req.Header.Set("Authorization", "Bearer test-management-key")
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusNotFound, rr.Body.String())
		}
	})

	t.Run("management control panel returns 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/management.html", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusNotFound, rr.Body.String())
		}
	})
}

func TestAmpProviderModelRoutes(t *testing.T) {
	testCases := []struct {
		name         string
		path         string
		wantStatus   int
		wantContains string
	}{
		{
			name:         "openai root models",
			path:         "/api/provider/openai/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "groq root models",
			path:         "/api/provider/groq/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "openai models",
			path:         "/api/provider/openai/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"object":"list"`,
		},
		{
			name:         "anthropic models",
			path:         "/api/provider/anthropic/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"data"`,
		},
		{
			name:         "google models v1",
			path:         "/api/provider/google/v1/models",
			wantStatus:   http.StatusOK,
			wantContains: `"models"`,
		},
		{
			name:         "google models v1beta",
			path:         "/api/provider/google/v1beta/models",
			wantStatus:   http.StatusOK,
			wantContains: `"models"`,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Authorization", "Bearer test-key")

			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)

			if rr.Code != tc.wantStatus {
				t.Fatalf("unexpected status code for %s: got %d want %d; body=%s", tc.path, rr.Code, tc.wantStatus, rr.Body.String())
			}
			if body := rr.Body.String(); !strings.Contains(body, tc.wantContains) {
				t.Fatalf("response body for %s missing %q: %s", tc.path, tc.wantContains, body)
			}
		})
	}
}

func TestModelsWithClientVersionReturnsCodexCatalog(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-client-version-catalog"
	modelRegistry.RegisterClient(clientID, "openai", []*registry.ModelInfo{
		{
			ID:            "gpt-5.5",
			Object:        "model",
			Created:       1776902400,
			OwnedBy:       "openai",
			Type:          "openai",
			DisplayName:   "GPT 5.5",
			Description:   "Frontier model for complex coding, research, and real-world work.",
			ContextLength: 272000,
			Thinking:      &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh"}},
		},
		{
			ID:            "custom-codex-model-test",
			Object:        "model",
			OwnedBy:       "test",
			Type:          "openai",
			DisplayName:   "Custom Codex Model",
			Description:   "Custom model from registry",
			ContextLength: 123456,
			Thinking:      &registry.ThinkingSupport{Levels: []string{"none", "minimal", "low", "medium", "unsupported", "high", "xhigh"}},
		},
		{ID: "grok-imagine-image-quality", Object: "model", OwnedBy: "xai", Type: "openai"},
		{ID: "gpt-image-2", Object: "model", OwnedBy: "openai", Type: "openai"},
		{ID: "grok-imagine-image", Object: "model", OwnedBy: "xai", Type: "openai"},
		{ID: "grok-imagine-video", Object: "model", OwnedBy: "xai", Type: "openai"},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientID)
	})

	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/models?client_version", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("User-Agent", "claude-cli/1.0")

	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var resp struct {
		Models []map[string]any `json:"models"`
		Object string           `json:"object"`
		Data   []any            `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response JSON: %v; body=%s", err, rr.Body.String())
	}
	if resp.Object != "" || resp.Data != nil {
		t.Fatalf("expected codex catalog format without object/data, got object=%q data=%v", resp.Object, resp.Data)
	}
	if len(resp.Models) == 0 {
		t.Fatal("expected codex catalog models")
	}

	var gpt55 map[string]any
	var custom map[string]any
	for _, model := range resp.Models {
		switch slug, _ := model["slug"].(string); slug {
		case "gpt-5.5":
			gpt55 = model
		case "custom-codex-model-test":
			custom = model
		}
	}
	if gpt55 == nil {
		t.Fatal("expected gpt-5.5 codex catalog entry")
	}
	if _, ok := gpt55["minimal_client_version"]; !ok {
		t.Fatal("expected minimal_client_version in codex catalog")
	}
	serviceTiers, ok := gpt55["service_tiers"].([]any)
	if !ok || len(serviceTiers) != 1 {
		t.Fatalf("expected gpt-5.5 priority service tier, got %#v", gpt55["service_tiers"])
	}
	if custom == nil {
		t.Fatal("expected custom model codex catalog entry")
	}
	if got, _ := custom["display_name"].(string); got != "Custom Codex Model" {
		t.Fatalf("custom display_name = %q, want Custom Codex Model", got)
	}
	if got, _ := custom["description"].(string); got != "Custom model from registry" {
		t.Fatalf("custom description = %q, want Custom model from registry", got)
	}
	if got, _ := custom["context_window"].(float64); got != 123456 {
		t.Fatalf("custom context_window = %v, want 123456", custom["context_window"])
	}
	assertCodexSupportedReasoningLevels(t, custom, []string{"none", "low", "medium", "high", "xhigh"})
	if custom["base_instructions"] != gpt55["base_instructions"] {
		t.Fatal("expected custom model to use gpt-5.5 base_instructions fallback")
	}
	if _, ok := custom["available_in_plans"].([]any); !ok {
		t.Fatalf("expected custom model to use gpt-5.5 available_in_plans fallback, got %#v", custom["available_in_plans"])
	}
	if got, _ := custom["prefer_websockets"].(bool); got {
		t.Fatalf("custom prefer_websockets = %v, want false", custom["prefer_websockets"])
	}
	if _, ok := custom["apply_patch_tool_type"]; ok {
		t.Fatal("expected custom model to omit apply_patch_tool_type")
	}
	if _, ok := custom["upgrade"]; ok {
		t.Fatal("expected custom model to omit upgrade")
	}
	if _, ok := custom["availability_nux"]; ok {
		t.Fatal("expected custom model to omit availability_nux")
	}

	hiddenModels := map[string]bool{
		"grok-imagine-image-quality": false,
		"gpt-image-2":                false,
		"grok-imagine-image":         false,
		"grok-imagine-video":         false,
	}
	for _, model := range resp.Models {
		slug, _ := model["slug"].(string)
		if _, ok := hiddenModels[slug]; !ok {
			continue
		}
		if visibility, _ := model["visibility"].(string); visibility != "hide" {
			t.Fatalf("%s visibility = %q, want hide", slug, visibility)
		}
		hiddenModels[slug] = true
	}
	for slug, found := range hiddenModels {
		if !found {
			t.Fatalf("expected hidden model %s in codex catalog", slug)
		}
	}
}

func assertCodexSupportedReasoningLevels(t *testing.T, model map[string]any, want []string) {
	t.Helper()

	rawLevels, ok := model["supported_reasoning_levels"].([]any)
	if !ok {
		t.Fatalf("expected supported_reasoning_levels, got %#v", model["supported_reasoning_levels"])
	}
	if len(rawLevels) != len(want) {
		t.Fatalf("supported_reasoning_levels length = %d, want %d: %#v", len(rawLevels), len(want), rawLevels)
	}
	for index, rawLevel := range rawLevels {
		levelEntry, ok := rawLevel.(map[string]any)
		if !ok {
			t.Fatalf("supported_reasoning_levels[%d] = %#v, want object", index, rawLevel)
		}
		if got, _ := levelEntry["effort"].(string); got != want[index] {
			t.Fatalf("supported_reasoning_levels[%d].effort = %q, want %q", index, got, want[index])
		}
	}
}

func TestDefaultRequestLoggerFactory_UsesResolvedLogDirectory(t *testing.T) {
	t.Setenv("WRITABLE_PATH", "")
	t.Setenv("writable_path", "")

	originalWD, errGetwd := os.Getwd()
	if errGetwd != nil {
		t.Fatalf("failed to get current working directory: %v", errGetwd)
	}

	tmpDir := t.TempDir()
	if errChdir := os.Chdir(tmpDir); errChdir != nil {
		t.Fatalf("failed to switch working directory: %v", errChdir)
	}
	defer func() {
		if errChdirBack := os.Chdir(originalWD); errChdirBack != nil {
			t.Fatalf("failed to restore working directory: %v", errChdirBack)
		}
	}()

	// Force ResolveLogDirectory to fallback to auth-dir/logs by making ./logs not a writable directory.
	if errWriteFile := os.WriteFile(filepath.Join(tmpDir, "logs"), []byte("not-a-directory"), 0o644); errWriteFile != nil {
		t.Fatalf("failed to create blocking logs file: %v", errWriteFile)
	}

	configDir := filepath.Join(tmpDir, "config")
	if errMkdirConfig := os.MkdirAll(configDir, 0o755); errMkdirConfig != nil {
		t.Fatalf("failed to create config dir: %v", errMkdirConfig)
	}
	configPath := filepath.Join(configDir, "config.yaml")

	authDir := filepath.Join(tmpDir, "auth")
	if errMkdirAuth := os.MkdirAll(authDir, 0o700); errMkdirAuth != nil {
		t.Fatalf("failed to create auth dir: %v", errMkdirAuth)
	}

	cfg := &proxyconfig.Config{
		SDKConfig: proxyconfig.SDKConfig{
			RequestLog: false,
		},
		AuthDir:           authDir,
		ErrorLogsMaxFiles: 10,
	}

	logger := defaultRequestLoggerFactory(cfg, configPath)
	fileLogger, ok := logger.(*internallogging.FileRequestLogger)
	if !ok {
		t.Fatalf("expected *FileRequestLogger, got %T", logger)
	}

	errLog := fileLogger.LogRequestWithOptions(
		"/v1/chat/completions",
		http.MethodPost,
		map[string][]string{"Content-Type": []string{"application/json"}},
		[]byte(`{"input":"hello"}`),
		http.StatusBadGateway,
		map[string][]string{"Content-Type": []string{"application/json"}},
		[]byte(`{"error":"upstream failure"}`),
		nil,
		nil,
		nil,
		nil,
		nil,
		true,
		"issue-1711",
		time.Now(),
		time.Now(),
	)
	if errLog != nil {
		t.Fatalf("failed to write forced error request log: %v", errLog)
	}

	authLogsDir := filepath.Join(authDir, "logs")
	authEntries, errReadAuthDir := os.ReadDir(authLogsDir)
	if errReadAuthDir != nil {
		t.Fatalf("failed to read auth logs dir %s: %v", authLogsDir, errReadAuthDir)
	}
	foundErrorLogInAuthDir := false
	for _, entry := range authEntries {
		if strings.HasPrefix(entry.Name(), "error-") && strings.HasSuffix(entry.Name(), ".log") {
			foundErrorLogInAuthDir = true
			break
		}
	}
	if !foundErrorLogInAuthDir {
		t.Fatalf("expected forced error log in auth fallback dir %s, got entries: %+v", authLogsDir, authEntries)
	}

	configLogsDir := filepath.Join(configDir, "logs")
	configEntries, errReadConfigDir := os.ReadDir(configLogsDir)
	if errReadConfigDir != nil && !os.IsNotExist(errReadConfigDir) {
		t.Fatalf("failed to inspect config logs dir %s: %v", configLogsDir, errReadConfigDir)
	}
	for _, entry := range configEntries {
		if strings.HasPrefix(entry.Name(), "error-") && strings.HasSuffix(entry.Name(), ".log") {
			t.Fatalf("unexpected forced error log in config dir %s", configLogsDir)
		}
	}
}
