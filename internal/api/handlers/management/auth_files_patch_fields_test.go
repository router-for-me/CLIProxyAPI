package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestPatchAuthFileFields_MergeHeadersAndDeleteEmptyValues(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:       "test.json",
		FileName: "test.json",
		Provider: "claude",
		Attributes: map[string]string{
			"path":            "/tmp/test.json",
			"header:X-Old":    "old",
			"header:X-Remove": "gone",
		},
		Metadata: map[string]any{
			"type": "claude",
			"headers": map[string]any{
				"X-Old":    "old",
				"X-Remove": "gone",
			},
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	body := `{"name":"test.json","prefix":"p1","proxy_url":"http://proxy.local","headers":{"X-Old":"new","X-New":"v","X-Remove":"  ","X-Nope":""}}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	updated, ok := manager.GetByID("test.json")
	if !ok || updated == nil {
		t.Fatalf("expected auth record to exist after patch")
	}

	if updated.Prefix != "p1" {
		t.Fatalf("prefix = %q, want %q", updated.Prefix, "p1")
	}
	if updated.ProxyURL != "http://proxy.local" {
		t.Fatalf("proxy_url = %q, want %q", updated.ProxyURL, "http://proxy.local")
	}

	if updated.Metadata == nil {
		t.Fatalf("expected metadata to be non-nil")
	}
	if got, _ := updated.Metadata["prefix"].(string); got != "p1" {
		t.Fatalf("metadata.prefix = %q, want %q", got, "p1")
	}
	if got, _ := updated.Metadata["proxy_url"].(string); got != "http://proxy.local" {
		t.Fatalf("metadata.proxy_url = %q, want %q", got, "http://proxy.local")
	}

	headersMeta, ok := updated.Metadata["headers"].(map[string]any)
	if !ok {
		raw, _ := json.Marshal(updated.Metadata["headers"])
		t.Fatalf("metadata.headers = %T (%s), want map[string]any", updated.Metadata["headers"], string(raw))
	}
	if got := headersMeta["X-Old"]; got != "new" {
		t.Fatalf("metadata.headers.X-Old = %#v, want %q", got, "new")
	}
	if got := headersMeta["X-New"]; got != "v" {
		t.Fatalf("metadata.headers.X-New = %#v, want %q", got, "v")
	}
	if _, ok := headersMeta["X-Remove"]; ok {
		t.Fatalf("expected metadata.headers.X-Remove to be deleted")
	}
	if _, ok := headersMeta["X-Nope"]; ok {
		t.Fatalf("expected metadata.headers.X-Nope to be absent")
	}

	if got := updated.Attributes["header:X-Old"]; got != "new" {
		t.Fatalf("attrs header:X-Old = %q, want %q", got, "new")
	}
	if got := updated.Attributes["header:X-New"]; got != "v" {
		t.Fatalf("attrs header:X-New = %q, want %q", got, "v")
	}
	if _, ok := updated.Attributes["header:X-Remove"]; ok {
		t.Fatalf("expected attrs header:X-Remove to be deleted")
	}
	if _, ok := updated.Attributes["header:X-Nope"]; ok {
		t.Fatalf("expected attrs header:X-Nope to be absent")
	}
}

func TestPatchAuthFileFields_HeadersEmptyMapIsNoop(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:       "noop.json",
		FileName: "noop.json",
		Provider: "claude",
		Attributes: map[string]string{
			"path":         "/tmp/noop.json",
			"header:X-Kee": "1",
		},
		Metadata: map[string]any{
			"type": "claude",
			"headers": map[string]any{
				"X-Kee": "1",
			},
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	body := `{"name":"noop.json","note":"hello","headers":{}}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	updated, ok := manager.GetByID("noop.json")
	if !ok || updated == nil {
		t.Fatalf("expected auth record to exist after patch")
	}
	if got := updated.Attributes["header:X-Kee"]; got != "1" {
		t.Fatalf("attrs header:X-Kee = %q, want %q", got, "1")
	}
	headersMeta, ok := updated.Metadata["headers"].(map[string]any)
	if !ok {
		t.Fatalf("expected metadata.headers to remain a map, got %T", updated.Metadata["headers"])
	}
	if got := headersMeta["X-Kee"]; got != "1" {
		t.Fatalf("metadata.headers.X-Kee = %#v, want %q", got, "1")
	}
}

func TestPatchAuthFileFields_PersistsExtendedFields(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	filePath := filepath.Join(authDir, "codex-auth.json")
	initial := `{
  "type": "codex",
  "email": "codex@example.com",
  "priority": 1,
  "note": "old note",
  "user-agent": "legacy-old-ua",
  "headers": {
    "X-Old": "1",
    "X-Remove": "gone"
  },
  "disable-cooling": true,
  "excluded-models": ["old-model"],
  "websocket": false
}`
	if err := os.WriteFile(filePath, []byte(initial), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	auth, err := h.buildAuthFromFileData(filePath, nil)
	if err != nil {
		t.Fatalf("buildAuthFromFileData() error = %v", err)
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	body := `{"name":"codex-auth.json","priority":0,"note":"new note","user_agent":"new-ua","headers":{"X-Old":"2","X-New":"3","X-Remove":""},"disable_cooling":false,"excluded_models":[" Model-B ","model-a","model-b"],"websockets":true}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var response struct {
		File map[string]any `json:"file"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Unmarshal(response) error = %v", err)
	}
	if response.File == nil {
		t.Fatalf("expected response.file to be present")
	}
	if got := response.File["user_agent"]; got != "new-ua" {
		t.Fatalf("response.file.user_agent = %#v, want %q", got, "new-ua")
	}
	if got, ok := response.File["priority"].(float64); !ok || got != 0 {
		t.Fatalf("response.file.priority = %#v, want 0", response.File["priority"])
	}
	if got, ok := response.File["websockets"].(bool); !ok || !got {
		t.Fatalf("response.file.websockets = %#v, want true", response.File["websockets"])
	}

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected updated auth to exist")
	}
	if got := updated.Attributes["priority"]; got != "0" {
		t.Fatalf("Attributes[priority] = %q, want %q", got, "0")
	}
	if got := updated.Attributes["note"]; got != "new note" {
		t.Fatalf("Attributes[note] = %q, want %q", got, "new note")
	}
	if got := updated.Attributes["header:User-Agent"]; got != "new-ua" {
		t.Fatalf("Attributes[header:User-Agent] = %q, want %q", got, "new-ua")
	}
	if got := updated.Attributes["websockets"]; got != "true" {
		t.Fatalf("Attributes[websockets] = %q, want %q", got, "true")
	}
	if got := updated.Attributes["excluded_models"]; got != "model-a,model-b" {
		t.Fatalf("Attributes[excluded_models] = %q, want %q", got, "model-a,model-b")
	}
	if got := updated.Attributes["excluded_models_hash"]; strings.TrimSpace(got) == "" {
		t.Fatal("expected excluded_models_hash to be populated")
	}
	if got, ok := updated.Metadata["disable_cooling"].(bool); !ok || got {
		t.Fatalf("Metadata[disable_cooling] = %#v, want false", updated.Metadata["disable_cooling"])
	}
	if _, ok := updated.Metadata["disable-cooling"]; ok {
		t.Fatal("Metadata[disable-cooling] should be removed")
	}
	if _, ok := updated.Metadata["user-agent"]; ok {
		t.Fatal("Metadata[user-agent] should be removed")
	}

	persisted, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(persisted, &document); err != nil {
		t.Fatalf("Unmarshal(file) error = %v", err)
	}
	if got, ok := document["priority"].(float64); !ok || got != 0 {
		t.Fatalf("file.priority = %#v, want 0", document["priority"])
	}
	if got, ok := document["note"].(string); !ok || got != "new note" {
		t.Fatalf("file.note = %#v, want %q", document["note"], "new note")
	}
	if got, ok := document["user_agent"].(string); !ok || got != "new-ua" {
		t.Fatalf("file.user_agent = %#v, want %q", document["user_agent"], "new-ua")
	}
	if _, ok := document["user-agent"]; ok {
		t.Fatal("file.user-agent should be removed")
	}
	if got, ok := document["disable_cooling"].(bool); !ok || got {
		t.Fatalf("file.disable_cooling = %#v, want false", document["disable_cooling"])
	}
	if _, ok := document["disable-cooling"]; ok {
		t.Fatal("file.disable-cooling should be removed")
	}
	if got, ok := document["websockets"].(bool); !ok || !got {
		t.Fatalf("file.websockets = %#v, want true", document["websockets"])
	}
	if _, ok := document["websocket"]; ok {
		t.Fatal("file.websocket should be removed")
	}
	excludedModels, ok := document["excluded_models"].([]any)
	if !ok {
		t.Fatalf("file.excluded_models = %T, want []any", document["excluded_models"])
	}
	if len(excludedModels) != 2 || excludedModels[0] != "model-a" || excludedModels[1] != "model-b" {
		t.Fatalf("file.excluded_models = %#v, want [model-a model-b]", excludedModels)
	}
	headers, ok := document["headers"].(map[string]any)
	if !ok {
		t.Fatalf("file.headers = %T, want map[string]any", document["headers"])
	}
	if got := headers["X-Old"]; got != "2" {
		t.Fatalf("file.headers.X-Old = %#v, want %q", got, "2")
	}
	if got := headers["X-New"]; got != "3" {
		t.Fatalf("file.headers.X-New = %#v, want %q", got, "3")
	}
	if _, ok := headers["X-Remove"]; ok {
		t.Fatal("file.headers.X-Remove should be removed")
	}
}

func TestParseOptionalJSONIntField_AcceptsZeroString(t *testing.T) {
	present, set, value, err := parseOptionalJSONIntField(json.RawMessage(`"0"`))
	if err != nil {
		t.Fatalf("parseOptionalJSONIntField() error = %v", err)
	}
	if !present || !set || value != 0 {
		t.Fatalf("unexpected result: present=%t set=%t value=%d", present, set, value)
	}
}

func TestParseOptionalJSONBoolField_SupportsFalseAndClear(t *testing.T) {
	present, set, value, err := parseOptionalJSONBoolField(json.RawMessage(`false`))
	if err != nil {
		t.Fatalf("parseOptionalJSONBoolField(false) error = %v", err)
	}
	if !present || !set || value {
		t.Fatalf("unexpected false result: present=%t set=%t value=%t", present, set, value)
	}

	present, set, value, err = parseOptionalJSONBoolField(json.RawMessage(`""`))
	if err != nil {
		t.Fatalf("parseOptionalJSONBoolField(\"\") error = %v", err)
	}
	if !present || set || value {
		t.Fatalf("unexpected clear result: present=%t set=%t value=%t", present, set, value)
	}
}
