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
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	return newTestServerWithOptions(t)
}

func newTestServerWithOptions(t *testing.T, opts ...ServerOption) *Server {
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
	}

	authManager := auth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()

	configPath := filepath.Join(tmpDir, "config.yaml")
	return NewServer(cfg, authManager, accessManager, configPath, opts...)
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

func TestManagementResponseExposesPluginSupportHeaderForCORS(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")

	server := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v0/management/config", nil)
	req.Header.Set("Origin", "http://127.0.0.1:5173")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusUnauthorized, rr.Body.String())
	}
	if got := rr.Header().Get("X-CPA-SUPPORT-PLUGIN"); got != pluginhost.SupportPluginHeaderValue() {
		t.Fatalf("X-CPA-SUPPORT-PLUGIN = %q, want %q", got, pluginhost.SupportPluginHeaderValue())
	}

	exposedHeaders := make(map[string]struct{})
	for _, headerName := range strings.Split(rr.Header().Get("Access-Control-Expose-Headers"), ",") {
		headerName = strings.ToLower(strings.TrimSpace(headerName))
		if headerName != "" {
			exposedHeaders[headerName] = struct{}{}
		}
	}
	for _, headerName := range corsExposedResponseHeaders {
		if _, ok := exposedHeaders[strings.ToLower(headerName)]; !ok {
			t.Fatalf("Access-Control-Expose-Headers missing %s: %q", headerName, rr.Header().Get("Access-Control-Expose-Headers"))
		}
	}
}

func TestNewServerWithPluginHostInjectsHandlerInterceptors(t *testing.T) {
	host := pluginhost.New()
	server := newTestServerWithOptions(t, WithPluginHost(host))

	if server.handlers == nil {
		t.Fatal("server handlers = nil")
	}
	got, ok := server.handlers.PluginHost.(*pluginhost.Host)
	if !ok || got != host {
		t.Fatalf("handler plugin host = %#v, want configured host", server.handlers.PluginHost)
	}
}

func TestNewServerWithoutPluginHostLeavesHandlerInterceptorsDisabled(t *testing.T) {
	server := newTestServer(t)

	if server.handlers == nil {
		t.Fatal("server handlers = nil")
	}
	if server.handlers.PluginHost != nil {
		t.Fatalf("handler plugin host = %#v, want nil", server.handlers.PluginHost)
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

	legacyReq := httptest.NewRequest(http.MethodGet, "/v0/management/usage?count=2", nil)
	legacyReq.Header.Set("Authorization", "Bearer test-management-key")
	legacyRR := httptest.NewRecorder()
	server.engine.ServeHTTP(legacyRR, legacyReq)
	if legacyRR.Code != http.StatusNotFound {
		t.Fatalf("legacy usage status = %d, want %d body=%s", legacyRR.Code, http.StatusNotFound, legacyRR.Body.String())
	}

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

func TestManagementPluginsRouteRegistered(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")

	server := newTestServer(t)
	enabled := true
	server.cfg.Plugins.Configs = map[string]proxyconfig.PluginInstanceConfig{
		"sample": {Enabled: &enabled, Priority: 4},
	}
	if errWrite := os.WriteFile(server.configFilePath, []byte("{}\n"), 0o600); errWrite != nil {
		t.Fatalf("failed to write config file: %v", errWrite)
	}

	req := httptest.NewRequest(http.MethodGet, "/v0/management/plugins", nil)
	req.Header.Set("Authorization", "Bearer test-management-key")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var payload struct {
		PluginsEnabled bool  `json:"plugins_enabled"`
		Plugins        []any `json:"plugins"`
	}
	if errUnmarshal := json.Unmarshal(rr.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v body=%s", errUnmarshal, rr.Body.String())
	}
	if payload.Plugins == nil {
		t.Fatalf("plugins field = nil, want array; body=%s", rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v0/management/plugins/sample/config", nil)
	req.Header.Set("Authorization", "Bearer test-management-key")
	rr = httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("config status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	var configPayload struct {
		Enabled  bool `json:"enabled"`
		Priority int  `json:"priority"`
	}
	if errUnmarshal := json.Unmarshal(rr.Body.Bytes(), &configPayload); errUnmarshal != nil {
		t.Fatalf("unmarshal config response: %v body=%s", errUnmarshal, rr.Body.String())
	}
	if !configPayload.Enabled || configPayload.Priority != 4 {
		t.Fatalf("plugin config = %#v, want enabled true priority 4", configPayload)
	}

	req = httptest.NewRequest(http.MethodDelete, "/v0/management/plugins/sample", nil)
	req.Header.Set("Authorization", "Bearer test-management-key")
	rr = httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
}

func TestVideosRoutesKeepXAINativeAndExposeOpenAIPrefix(t *testing.T) {
	server := newTestServer(t)

	nativeReq := httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"model":"sora-2","prompt":"make a video"}`))
	nativeReq.Header.Set("Authorization", "Bearer test-key")
	nativeReq.Header.Set("Content-Type", "application/json")
	nativeRR := httptest.NewRecorder()
	server.engine.ServeHTTP(nativeRR, nativeReq)
	if nativeRR.Code != http.StatusBadRequest {
		t.Fatalf("native status = %d, want %d body=%s", nativeRR.Code, http.StatusBadRequest, nativeRR.Body.String())
	}
	if !strings.Contains(nativeRR.Body.String(), "/v1/videos/generations") {
		t.Fatalf("expected /v1/videos to keep xAI native validation, body=%s", nativeRR.Body.String())
	}

	openAIReq := httptest.NewRequest(http.MethodPost, "/openai/v1/videos", strings.NewReader(`{"model":`))
	openAIReq.Header.Set("Authorization", "Bearer test-key")
	openAIReq.Header.Set("Content-Type", "application/json")
	openAIRR := httptest.NewRecorder()
	server.engine.ServeHTTP(openAIRR, openAIReq)
	if openAIRR.Code != http.StatusBadRequest {
		t.Fatalf("openai create status = %d, want %d body=%s", openAIRR.Code, http.StatusBadRequest, openAIRR.Body.String())
	}
	if !strings.Contains(openAIRR.Body.String(), "body must be valid JSON") {
		t.Fatalf("expected /openai/v1/videos create handler, body=%s", openAIRR.Body.String())
	}

	contentReq := httptest.NewRequest(http.MethodGet, "/openai/v1/videos/video_123/content?variant=thumbnail", nil)
	contentReq.Header.Set("Authorization", "Bearer test-key")
	contentRR := httptest.NewRecorder()
	server.engine.ServeHTTP(contentRR, contentReq)
	if contentRR.Code != http.StatusBadRequest {
		t.Fatalf("content status = %d, want %d body=%s", contentRR.Code, http.StatusBadRequest, contentRR.Body.String())
	}
	if !strings.Contains(contentRR.Body.String(), "variant") {
		t.Fatalf("expected /openai/v1/videos content handler, body=%s", contentRR.Body.String())
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

func TestModelsDispatchByAnthropicVersionHeader(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-anthropic-version-dispatch"
	modelRegistry.RegisterClient(clientID, "claude", []*registry.ModelInfo{
		{
			ID:                  "claude-sonnet-4-6",
			Object:              "model",
			OwnedBy:             "anthropic",
			Type:                "claude",
			DisplayName:         "Claude 4.6 Sonnet",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
		},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientID)
	})

	server := newTestServer(t)

	// Anthropic API request (Anthropic-Version header, non-claude-cli User-Agent) -> Claude format.
	t.Run("anthropic version header routes to claude format", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer test-key")
		req.Header.Set("User-Agent", "Zed/1.0")
		req.Header.Set("Anthropic-Version", "2023-06-01")

		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}

		var resp struct {
			Object  string           `json:"object"`
			HasMore *bool            `json:"has_more"`
			Data    []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response JSON: %v; body=%s", err, rr.Body.String())
		}
		if resp.Object == "list" {
			t.Fatalf("expected Claude format (no object=list), got OpenAI format: %s", rr.Body.String())
		}
		if resp.HasMore == nil {
			t.Fatalf("expected Claude envelope with has_more, got %s", rr.Body.String())
		}

		var claudeModel map[string]any
		for _, m := range resp.Data {
			if id, _ := m["id"].(string); id == "claude-sonnet-4-6" {
				claudeModel = m
			}
		}
		if claudeModel == nil {
			t.Fatalf("expected claude-sonnet-4-6 in response, got %s", rr.Body.String())
		}
		for _, field := range []string{"max_input_tokens", "max_tokens", "display_name"} {
			if _, ok := claudeModel[field]; !ok {
				t.Fatalf("expected Claude model to include %q, got %v", field, claudeModel)
			}
		}
	})

	// Plain request (no Anthropic-Version, non-claude-cli User-Agent) -> OpenAI format, unaffected.
	t.Run("plain request stays on openai format", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer test-key")
		req.Header.Set("User-Agent", "Mozilla/5.0")

		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}

		var resp struct {
			Object string           `json:"object"`
			Data   []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response JSON: %v; body=%s", err, rr.Body.String())
		}
		if resp.Object != "list" {
			t.Fatalf("expected OpenAI format (object=list), got %s", rr.Body.String())
		}
		for _, m := range resp.Data {
			if _, ok := m["max_input_tokens"]; ok {
				t.Fatalf("did not expect max_input_tokens in OpenAI format, got %v", m)
			}
		}
	})
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
		{ID: "grok-imagine-video-1.5-preview", Object: "model", OwnedBy: "xai", Type: "openai"},
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
		"grok-imagine-image-quality":     false,
		"gpt-image-2":                    false,
		"grok-imagine-image":             false,
		"grok-imagine-video":             false,
		"grok-imagine-video-1.5-preview": false,
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

func TestFormatHomeClaudeModelIncludesAnthropicSchemaFields(t *testing.T) {
	withMetadata := formatHomeClaudeModel(homeModelEntry{
		id:                  "claude-sonnet-4-6",
		created:             1771372800,
		ownedBy:             "anthropic",
		displayName:         "Claude 4.6 Sonnet",
		contextLength:       200000,
		maxCompletionTokens: 64000,
	})
	if got := withMetadata["created_at"]; got != "2026-02-18T00:00:00Z" {
		t.Fatalf("created_at = %v, want RFC3339 timestamp", got)
	}
	if got := withMetadata["type"]; got != "model" {
		t.Fatalf("type = %v, want model", got)
	}
	if got := withMetadata["display_name"]; got != "Claude 4.6 Sonnet" {
		t.Fatalf("display_name = %v, want Claude 4.6 Sonnet", got)
	}
	if got := withMetadata["max_input_tokens"]; got != 200000 {
		t.Fatalf("max_input_tokens = %v, want 200000", got)
	}
	if got := withMetadata["max_tokens"]; got != 64000 {
		t.Fatalf("max_tokens = %v, want 64000", got)
	}

	withDefaults := formatHomeClaudeModel(homeModelEntry{id: "claude-no-limits"})
	if got := withDefaults["display_name"]; got != "claude-no-limits" {
		t.Fatalf("display_name fallback = %v, want claude-no-limits", got)
	}
	if got := withDefaults["max_input_tokens"]; got != registry.DefaultClaudeMaxInputTokens {
		t.Fatalf("max_input_tokens fallback = %v, want %d", got, registry.DefaultClaudeMaxInputTokens)
	}
	if got := withDefaults["max_tokens"]; got != registry.DefaultClaudeMaxOutputTokens {
		t.Fatalf("max_tokens fallback = %v, want %d", got, registry.DefaultClaudeMaxOutputTokens)
	}
	if _, ok := withDefaults["created_at"]; ok {
		t.Fatalf("created_at should be omitted when source created is missing, got %v", withDefaults)
	}
}

func TestDecodeHomeModelsKeepsTokenMetadata(t *testing.T) {
	entries, errDecode := decodeHomeModels([]byte(`{
		"claude": [
			{
				"id": "claude-sonnet-4-6",
				"created": 1771372800,
				"owned_by": "anthropic",
				"context_length": 200000,
				"max_completion_tokens": 64000
			}
		],
		"gemini": [
			{
				"name": "models/gemini-3-pro",
				"inputTokenLimit": 1048576,
				"outputTokenLimit": 65536
			}
		]
	}`))
	if errDecode != nil {
		t.Fatalf("decodeHomeModels returned error: %v", errDecode)
	}

	byID := make(map[string]homeModelEntry, len(entries))
	for _, entry := range entries {
		byID[entry.id] = entry
	}
	claudeEntry, ok := byID["claude-sonnet-4-6"]
	if !ok {
		t.Fatalf("expected claude-sonnet-4-6 entry, got %v", byID)
	}
	if claudeEntry.contextLength != 200000 || claudeEntry.maxCompletionTokens != 64000 {
		t.Fatalf("claude token metadata = %d/%d, want 200000/64000", claudeEntry.contextLength, claudeEntry.maxCompletionTokens)
	}
	geminiEntry, ok := byID["gemini-3-pro"]
	if !ok {
		t.Fatalf("expected gemini-3-pro entry, got %v", byID)
	}
	if geminiEntry.contextLength != 1048576 || geminiEntry.maxCompletionTokens != 65536 {
		t.Fatalf("gemini token metadata = %d/%d, want 1048576/65536", geminiEntry.contextLength, geminiEntry.maxCompletionTokens)
	}
}

func TestHomeModelsAuthStatus(t *testing.T) {
	cases := []struct {
		name        string
		raw         string
		wantStatus  int
		wantHandled bool
	}{
		{"no credentials", `{"error":{"type":"no_credentials","message":"Missing API key"}}`, http.StatusUnauthorized, true},
		{"invalid credential", `{"error":{"type":"invalid_credential","message":"Invalid API key"}}`, http.StatusUnauthorized, true},
		{"internal error maps to bad gateway", `{"error":{"type":"internal_error","message":"boom"}}`, http.StatusBadGateway, true},
		{"models payload not an error", `{"openai":[{"id":"gpt-5.5"}]}`, 0, false},
		{"empty payload not an error", `{}`, 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, handled := homeModelsAuthStatus([]byte(tc.raw))
			if handled != tc.wantHandled {
				t.Fatalf("handled = %v, want %v (status=%d)", handled, tc.wantHandled, status)
			}
			if handled && status != tc.wantStatus {
				t.Fatalf("status = %d, want %d", status, tc.wantStatus)
			}
		})
	}
}

func TestHomeModelsErrorMessage(t *testing.T) {
	if msg := homeModelsErrorMessage([]byte(`{"error":{"type":"invalid_credential","message":"Invalid API key"}}`)); msg != "Invalid API key" {
		t.Fatalf("message = %q, want %q", msg, "Invalid API key")
	}
	if msg := homeModelsErrorMessage([]byte(`{"openai":[]}`)); msg != "home models request failed" {
		t.Fatalf("default message = %q, want fallback", msg)
	}
}
