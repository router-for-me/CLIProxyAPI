package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"gopkg.in/yaml.v3"
)

func TestSyncOpenAICompatModels_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"kimi-k2.5"},{"id":"GLM-5.1"}]}`))
	}))
	defer upstream.Close()

	modelScope := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		search := r.URL.Query().Get("search")
		switch search {
		case "kimi-k2.5":
			_, _ = w.Write([]byte(`{"success":true,"data":{"models":[{"id":"moonshotai/Kimi-K2.5","display_name":"Kimi-K2.5","downloads":100}]}}`))
		case "GLM-5.1":
			_, _ = w.Write([]byte(`{"success":true,"data":{"models":[{"id":"zhipuai/GLM-5.1","display_name":"GLM-5.1","downloads":80}]}}`))
		default:
			_, _ = w.Write([]byte(`{"success":true,"data":{"models":[]}}`))
		}
	}))
	defer modelScope.Close()

	restore := swapModelScopeBaseURL(modelScope.URL)
	defer restore()

	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:          "us-ci",
			BaseURL:       upstream.URL + "/v1",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "k1"}},
			Models:        []config.OpenAICompatibilityModel{{Name: "old-model"}},
		}},
	}
	h, _ := newSyncTestHandler(t, cfg)

	status, body := performSyncRequest(t, h, `{"name":"us-ci"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", status, body)
	}
	if body["status"] != "ok" {
		t.Fatalf("status field = %v, want ok", body["status"])
	}
	assertNumberField(t, body, "updated_count", 2)
	assertNumberField(t, body, "fetched_count", 2)

	models := h.cfg.OpenAICompatibility[0].Models
	if len(models) != 2 {
		t.Fatalf("models len = %d, want 2", len(models))
	}

	got := make(map[string]string, len(models))
	for _, model := range models {
		got[model.Name] = model.Alias
	}
	if got["kimi-k2.5"] != "Kimi-K2.5" {
		t.Fatalf("alias(kimi-k2.5) = %q, want %q", got["kimi-k2.5"], "Kimi-K2.5")
	}
	if got["GLM-5.1"] != "" {
		t.Fatalf("alias(GLM-5.1) = %q, want empty", got["GLM-5.1"])
	}
}

func TestSyncOpenAICompatModels_MultiKeyUnion(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer key-A":
			_, _ = w.Write([]byte(`{"data":[{"id":"Model-A"}]}`))
		case "Bearer key-B":
			_, _ = w.Write([]byte(`{"data":[{"id":"Model-B"}]}`))
		default:
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}
	}))
	defer upstream.Close()

	modelScope := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		search := r.URL.Query().Get("search")
		_, _ = w.Write([]byte(`{"success":true,"data":{"models":[{"id":"` + search + `","display_name":"` + search + `","downloads":1}]}}`))
	}))
	defer modelScope.Close()

	restore := swapModelScopeBaseURL(modelScope.URL)
	defer restore()

	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "us-ci",
			BaseURL: upstream.URL,
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{
				{APIKey: "key-A"},
				{APIKey: "key-B"},
			},
		}},
	}
	h, _ := newSyncTestHandler(t, cfg)

	status, body := performSyncRequest(t, h, `{"name":"us-ci"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", status, body)
	}
	assertNumberField(t, body, "updated_count", 2)
	assertNumberField(t, body, "fetched_count", 2)

	models := h.cfg.OpenAICompatibility[0].Models
	if len(models) != 2 {
		t.Fatalf("models len = %d, want 2", len(models))
	}
	names := []string{models[0].Name, models[1].Name}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "Model-A") || !strings.Contains(joined, "Model-B") {
		t.Fatalf("models = %v, want both Model-A and Model-B", names)
	}
}

func TestSyncOpenAICompatModels_NormalizedModelScopeMatch(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"Kimi_K2.5"}]}`))
	}))
	defer upstream.Close()

	modelScope := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"models":[{"id":"moonshotai/Kimi-K2.5","display_name":"Kimi-K2.5","downloads":99}]}}`))
	}))
	defer modelScope.Close()

	restore := swapModelScopeBaseURL(modelScope.URL)
	defer restore()

	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:          "us-ci",
			BaseURL:       upstream.URL,
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "k"}},
		}},
	}
	h, _ := newSyncTestHandler(t, cfg)

	status, body := performSyncRequest(t, h, `{"name":"us-ci"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", status, body)
	}
	models := h.cfg.OpenAICompatibility[0].Models
	if len(models) != 1 {
		t.Fatalf("models len = %d, want 1", len(models))
	}
	if models[0].Alias != "Kimi-K2.5" {
		t.Fatalf("alias = %q, want %q", models[0].Alias, "Kimi-K2.5")
	}
}

func TestSyncOpenAICompatModels_AliasOmitAndWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"Exact-Name"},{"id":"lower_name"}]}`))
	}))
	defer upstream.Close()

	modelScope := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		search := r.URL.Query().Get("search")
		switch search {
		case "Exact-Name":
			_, _ = w.Write([]byte(`{"success":true,"data":{"models":[{"id":"repo/Exact-Name","display_name":"Exact-Name","downloads":30}]}}`))
		case "lower_name":
			_, _ = w.Write([]byte(`{"success":true,"data":{"models":[{"id":"repo/lower-name","display_name":"Lower-Name","downloads":20}]}}`))
		default:
			_, _ = w.Write([]byte(`{"success":true,"data":{"models":[]}}`))
		}
	}))
	defer modelScope.Close()

	restore := swapModelScopeBaseURL(modelScope.URL)
	defer restore()

	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:          "us-ci",
			BaseURL:       upstream.URL,
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "k"}},
		}},
	}
	h, _ := newSyncTestHandler(t, cfg)

	status, body := performSyncRequest(t, h, `{"name":"us-ci"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", status, body)
	}

	got := make(map[string]string)
	for _, model := range h.cfg.OpenAICompatibility[0].Models {
		got[model.Name] = model.Alias
	}
	if got["Exact-Name"] != "" {
		t.Fatalf("alias(Exact-Name) = %q, want empty", got["Exact-Name"])
	}
	if got["lower_name"] != "Lower-Name" {
		t.Fatalf("alias(lower_name) = %q, want %q", got["lower_name"], "Lower-Name")
	}
}

func TestSyncOpenAICompatModels_UnmatchedFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"unknown-model"}]}`))
	}))
	defer upstream.Close()

	modelScope := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"models":[{"id":"repo/something-else","display_name":"something-else","downloads":1}]}}`))
	}))
	defer modelScope.Close()

	restore := swapModelScopeBaseURL(modelScope.URL)
	defer restore()

	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:          "us-ci",
			BaseURL:       upstream.URL,
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "k"}},
		}},
	}
	h, _ := newSyncTestHandler(t, cfg)

	status, body := performSyncRequest(t, h, `{"name":"us-ci"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", status, body)
	}

	models := h.cfg.OpenAICompatibility[0].Models
	if len(models) != 1 {
		t.Fatalf("models len = %d, want 1", len(models))
	}
	if models[0].Name != "unknown-model" || models[0].Alias != "" {
		t.Fatalf("model = %+v, want name=unknown-model alias=''", models[0])
	}

	unmatchedRaw, ok := body["unmatched_models"].(map[string]any)
	if !ok {
		t.Fatalf("unmatched_models type = %T, want object", body["unmatched_models"])
	}
	unmatchedListRaw, ok := unmatchedRaw["us-ci"].([]any)
	if !ok || len(unmatchedListRaw) != 1 || unmatchedListRaw[0] != "unknown-model" {
		t.Fatalf("unmatched_models[us-ci] = %v, want [unknown-model]", unmatchedRaw["us-ci"])
	}
}

func TestSyncOpenAICompatModels_ModelScopeFailureDoesNotPersist(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"new-model"}]}`))
	}))
	defer upstream.Close()

	modelScope := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer modelScope.Close()

	restore := swapModelScopeBaseURL(modelScope.URL)
	defer restore()

	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:          "us-ci",
			BaseURL:       upstream.URL,
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "k"}},
			Models:        []config.OpenAICompatibilityModel{{Name: "old-model"}},
		}},
	}
	h, configPath := newSyncTestHandler(t, cfg)

	status, body := performSyncRequest(t, h, `{"name":"us-ci"}`)
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502, body=%s", status, body)
	}

	models := h.cfg.OpenAICompatibility[0].Models
	if len(models) != 1 || models[0].Name != "old-model" {
		t.Fatalf("in-memory models changed unexpectedly: %+v", models)
	}

	content, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("read config file: %v", errRead)
	}
	if !strings.Contains(string(content), "old-model") {
		t.Fatalf("config file changed unexpectedly: %s", string(content))
	}
}

func TestSyncOpenAICompatModels_ErrorPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("invalid params", func(t *testing.T) {
		cfg := &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:    "us-ci",
				BaseURL: "https://example.com/v1",
			}},
		}
		h, _ := newSyncTestHandler(t, cfg)
		status, _ := performSyncRequest(t, h, `{}`)
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", status)
		}
	})

	t.Run("provider not found", func(t *testing.T) {
		cfg := &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:    "us-ci",
				BaseURL: "https://example.com/v1",
			}},
		}
		h, _ := newSyncTestHandler(t, cfg)
		status, _ := performSyncRequest(t, h, `{"name":"missing-provider"}`)
		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", status)
		}
	})

	t.Run("all upstream credentials failed", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "upstream failed", http.StatusInternalServerError)
		}))
		defer upstream.Close()

		modelScope := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"success":true,"data":{"models":[]}}`))
		}))
		defer modelScope.Close()
		restore := swapModelScopeBaseURL(modelScope.URL)
		defer restore()

		cfg := &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:    "us-ci",
				BaseURL: upstream.URL,
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "k1"},
					{APIKey: "k2"},
				},
				Models: []config.OpenAICompatibilityModel{{Name: "old-model"}},
			}},
		}
		h, _ := newSyncTestHandler(t, cfg)

		status, body := performSyncRequest(t, h, `{"name":"us-ci"}`)
		if status != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502, body=%s", status, body)
		}
		models := h.cfg.OpenAICompatibility[0].Models
		if len(models) != 1 || models[0].Name != "old-model" {
			t.Fatalf("models changed unexpectedly: %+v", models)
		}
	})
}

func TestSyncOpenAICompatModels_PreviewRawModels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"kimi-k2.5"},{"id":"GLM-5.1"}]}`))
	}))
	defer upstream.Close()

	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:          "us-ci",
			BaseURL:       upstream.URL,
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "k"}},
			Models:        []config.OpenAICompatibilityModel{{Name: "old-model"}},
		}},
	}
	h, configPath := newSyncTestHandler(t, cfg)

	status, body := performSyncRequest(t, h, `{"name":"us-ci","preview":true,"skip_alias_lookup":true}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%v", status, body)
	}
	if int(body["fetched_count"].(float64)) != 2 {
		t.Fatalf("fetched_count = %v, want 2", body["fetched_count"])
	}
	if _, ok := body["models"].([]any); !ok {
		t.Fatalf("models type = %T, want array", body["models"])
	}
	if len(h.cfg.OpenAICompatibility[0].Models) != 1 || h.cfg.OpenAICompatibility[0].Models[0].Name != "old-model" {
		t.Fatalf("preview should not mutate config: %+v", h.cfg.OpenAICompatibility[0].Models)
	}
	content, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("read config file: %v", errRead)
	}
	if !strings.Contains(string(content), "old-model") {
		t.Fatalf("preview unexpectedly rewrote config: %s", string(content))
	}
}

func TestSyncOpenAICompatModels_ConfirmReplaceModels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "us-ci",
			BaseURL: "https://example.com/v1",
			Models:  []config.OpenAICompatibilityModel{{Name: "old-model", Alias: "Old"}},
		}},
	}
	h, _ := newSyncTestHandler(t, cfg)

	status, body := performSyncRequest(t, h, `{
		"name":"us-ci",
		"preview":false,
		"selected_models":[
			{"name":"kimi-k2.5","alias":"Kimi-K2.5"},
			{"name":"glm-5.1","alias":"GLM-5.1"}
		]
	}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%v", status, body)
	}
	models := h.cfg.OpenAICompatibility[0].Models
	if len(models) != 2 || models[0].Name == "old-model" {
		t.Fatalf("models = %+v, want replaced list", models)
	}
}

func TestLookupOpenAICompatAliases_MixedResults(t *testing.T) {
	gin.SetMode(gin.TestMode)

	modelScope := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("search") {
		case "kimi-k2.5":
			_, _ = w.Write([]byte(`{"success":true,"data":{"models":[{"id":"moonshotai/Kimi-K2.5","display_name":"Kimi-K2.5","downloads":100}]}}`))
		default:
			_, _ = w.Write([]byte(`{"success":true,"data":{"models":[]}}`))
		}
	}))
	defer modelScope.Close()

	restore := swapModelScopeBaseURL(modelScope.URL)
	defer restore()

	h, _ := newSyncTestHandler(t, &config.Config{})
	status, body := performLookupAliasesRequest(t, h, `{"models":["kimi-k2.5","unknown-model"]}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%v", status, body)
	}
	matched := body["matched"].([]any)
	unmatched := body["unmatched"].([]any)
	if len(matched) != 1 || len(unmatched) != 1 {
		t.Fatalf("matched=%v unmatched=%v", matched, unmatched)
	}
}

func TestLookupOpenAICompatAliases_RejectsEmptyModels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h, _ := newSyncTestHandler(t, &config.Config{})
	status, _ := performLookupAliasesRequest(t, h, `{"models":[]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
}

func newSyncTestHandler(t *testing.T, cfg *config.Config) (*Handler, string) {
	t.Helper()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	data, errMarshal := yaml.Marshal(cfg)
	if errMarshal != nil {
		t.Fatalf("marshal config: %v", errMarshal)
	}
	if errWrite := os.WriteFile(configPath, data, 0644); errWrite != nil {
		t.Fatalf("write config file: %v", errWrite)
	}
	return &Handler{
		cfg:            cfg,
		configFilePath: configPath,
	}, configPath
}

func performSyncRequest(t *testing.T, h *Handler, body string) (int, map[string]any) {
	t.Helper()

	router := gin.New()
	router.POST("/v0/management/openai-compatibility/sync-models", h.SyncOpenAICompatModels)

	req := httptest.NewRequest(http.MethodPost, "/v0/management/openai-compatibility/sync-models", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	var parsed map[string]any
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &parsed); errDecode != nil {
		t.Fatalf("decode response: %v body=%s", errDecode, recorder.Body.String())
	}
	return recorder.Code, parsed
}

func performLookupAliasesRequest(t *testing.T, h *Handler, body string) (int, map[string]any) {
	t.Helper()

	router := gin.New()
	router.POST("/v0/management/openai-compatibility/lookup-aliases", h.LookupOpenAICompatAliases)

	req := httptest.NewRequest(http.MethodPost, "/v0/management/openai-compatibility/lookup-aliases", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	var parsed map[string]any
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &parsed); errDecode != nil {
		t.Fatalf("decode response: %v body=%s", errDecode, recorder.Body.String())
	}
	return recorder.Code, parsed
}

func assertNumberField(t *testing.T, body map[string]any, key string, want int) {
	t.Helper()

	value, ok := body[key]
	if !ok {
		t.Fatalf("missing key %q in response", key)
	}
	number, ok := value.(float64)
	if !ok {
		t.Fatalf("key %q type = %T, want number", key, value)
	}
	if int(number) != want {
		t.Fatalf("key %q = %d, want %d", key, int(number), want)
	}
}

func swapModelScopeBaseURL(newURL string) func() {
	old := modelScopeOpenAPIBaseURL
	modelScopeOpenAPIBaseURL = newURL
	return func() {
		modelScopeOpenAPIBaseURL = old
	}
}
