package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type managementPriorityStore struct {
	mu    sync.Mutex
	auths map[string]*coreauth.Auth
}

func (s *managementPriorityStore) List(context.Context) ([]*coreauth.Auth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*coreauth.Auth, 0, len(s.auths))
	for _, auth := range s.auths {
		out = append(out, auth.Clone())
	}
	return out, nil
}

func (s *managementPriorityStore) Save(_ context.Context, auth *coreauth.Auth) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.auths == nil {
		s.auths = make(map[string]*coreauth.Auth)
	}
	s.auths[auth.ID] = auth.Clone()
	return auth.ID, nil
}

func (*managementPriorityStore) Delete(context.Context, string) error { return nil }

func (s *managementPriorityStore) PersistMutation(_ context.Context, before, after *coreauth.Auth) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.auths[before.ID]
	if current == nil || current.Revision() != before.Revision() {
		return "", coreauth.ErrAuthSourceConflict
	}
	s.auths[after.ID] = after.Clone()
	return after.ID, nil
}

func TestListAuthFilesIncludesRevisionAndExactPriorityPresence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
	registered, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:         "synthetic-auth.json",
		FileName:   "synthetic-auth.json",
		Provider:   "codex",
		Attributes: map[string]string{"path": t.TempDir() + "/synthetic-auth.json"},
		Metadata:   map[string]any{"type": "codex"},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	h.ListAuthFiles(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if err = json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Files) != 1 {
		t.Fatalf("files len = %d, want 1", len(payload.Files))
	}
	entry := payload.Files[0]
	if got := entry["revision"]; got != registered.Revision() {
		t.Fatalf("revision = %#v, want %q", got, registered.Revision())
	}
	if got := entry["priority_present"]; got != false {
		t.Fatalf("priority_present = %#v, want false", got)
	}
	if got := entry["priority"]; got != float64(0) {
		t.Fatalf("priority = %#v, want runtime default 0", got)
	}
}

func TestPatchAuthFilePrioritySetsPriorityConditionally(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &managementPriorityStore{}
	manager := coreauth.NewManager(store, &coreauth.FillFirstSelector{}, nil)
	registered, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:         "synthetic-auth.json",
		FileName:   "synthetic-auth.json",
		Provider:   "codex",
		Attributes: map[string]string{"path": "/synthetic/synthetic-auth.json", "priority": "10"},
		Metadata: map[string]any{
			"type":         "codex",
			"priority":     float64(10),
			"access_token": "synthetic-token",
		},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	body, err := json.Marshal(map[string]any{
		"name":              registered.FileName,
		"expected_revision": registered.Revision(),
		"operation":         "set",
		"priority":          101,
	})
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/priority", bytes.NewReader(body))
	h.PatchAuthFilePriority(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var payload struct {
		Status    string `json:"status"`
		ID        string `json:"id"`
		Name      string `json:"name"`
		Revision  string `json:"revision"`
		Persisted bool   `json:"persisted"`
		Priority  struct {
			Present bool `json:"present"`
			Value   int  `json:"value"`
		} `json:"priority"`
	}
	if err = json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Status != "ok" || payload.ID != registered.ID || payload.Name != registered.FileName || !payload.Persisted {
		t.Fatalf("unexpected response: %#v", payload)
	}
	if payload.Revision == "" || payload.Revision == registered.Revision() {
		t.Fatalf("revision = %q, want rotated token", payload.Revision)
	}
	if !payload.Priority.Present || payload.Priority.Value != 101 {
		t.Fatalf("priority = %#v, want present 101", payload.Priority)
	}

	current, ok := manager.GetByID(registered.ID)
	if !ok {
		t.Fatal("GetByID() missing auth")
	}
	if got := current.Metadata["access_token"]; got != "synthetic-token" {
		t.Fatalf("access_token changed = %#v", got)
	}
}

func TestPatchAuthFilePriorityUnsetsExactFieldAndOmitsValue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &managementPriorityStore{}
	manager := coreauth.NewManager(store, &coreauth.FillFirstSelector{}, nil)
	registered, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:         "synthetic-auth.json",
		FileName:   "synthetic-auth.json",
		Provider:   "codex",
		Attributes: map[string]string{"path": "/synthetic/synthetic-auth.json", "priority": "10"},
		Metadata:   map[string]any{"type": "codex", "priority": float64(10), "note": "keep"},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	body, _ := json.Marshal(map[string]any{
		"name":              registered.FileName,
		"expected_revision": registered.Revision(),
		"operation":         "unset",
	})
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/priority", bytes.NewReader(body))
	h.PatchAuthFilePriority(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var payload map[string]any
	if err = json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	priority, ok := payload["priority"].(map[string]any)
	if !ok || priority["present"] != false {
		t.Fatalf("priority response = %#v, want absent", payload["priority"])
	}
	if _, ok = priority["value"]; ok {
		t.Fatalf("unset response included value: %#v", priority)
	}
	current, _ := manager.GetByID(registered.ID)
	if _, ok = current.Metadata["priority"]; ok {
		t.Fatalf("priority metadata still present: %#v", current.Metadata)
	}
	if _, ok = current.Attributes["priority"]; ok {
		t.Fatalf("priority attribute still present: %#v", current.Attributes)
	}
	if got := current.Metadata["note"]; got != "keep" {
		t.Fatalf("note changed = %#v", got)
	}
}

func TestPatchAuthFilePriorityRejectsStaleRevisionWithoutMutation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &managementPriorityStore{}
	manager := coreauth.NewManager(store, &coreauth.FillFirstSelector{}, nil)
	registered, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:         "synthetic-auth.json",
		FileName:   "synthetic-auth.json",
		Provider:   "codex",
		Attributes: map[string]string{"path": "/synthetic/synthetic-auth.json", "priority": "10"},
		Metadata:   map[string]any{"type": "codex", "priority": float64(10)},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	body := []byte(`{"name":"synthetic-auth.json","expected_revision":"stale","operation":"set","priority":101}`)
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/priority", bytes.NewReader(body))
	h.PatchAuthFilePriority(ctx)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusConflict, recorder.Body.String())
	}
	current, _ := manager.GetByID(registered.ID)
	if current.Revision() != registered.Revision() || current.Attributes["priority"] != "10" {
		t.Fatalf("stale mutation changed auth: revision=%q priority=%q", current.Revision(), current.Attributes["priority"])
	}
}

func TestPatchAuthFilePriorityRejectsAmbiguousNumbers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &managementPriorityStore{}
	manager := coreauth.NewManager(store, &coreauth.FillFirstSelector{}, nil)
	registered, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:         "synthetic-auth.json",
		FileName:   "synthetic-auth.json",
		Provider:   "codex",
		Attributes: map[string]string{"path": "/synthetic/synthetic-auth.json", "priority": "10"},
		Metadata:   map[string]any{"type": "codex", "priority": float64(10)},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	for _, rawPriority := range []string{`1.5`, `1e2`, `"101"`, `-1`, `2147483648`, `null`} {
		t.Run(rawPriority, func(t *testing.T) {
			body := []byte(`{"name":"synthetic-auth.json","expected_revision":"` + registered.Revision() + `","operation":"set","priority":` + rawPriority + `}`)
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/priority", bytes.NewReader(body))
			h.PatchAuthFilePriority(ctx)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
		})
	}
}

func TestPatchAuthFilePriorityRejectsDuplicateJSONFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &managementPriorityStore{}
	manager := coreauth.NewManager(store, &coreauth.FillFirstSelector{}, nil)
	registered, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:         "synthetic-auth.json",
		FileName:   "synthetic-auth.json",
		Provider:   "codex",
		Attributes: map[string]string{"path": "/synthetic/synthetic-auth.json", "priority": "10"},
		Metadata:   map[string]any{"type": "codex", "priority": float64(10)},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	body := []byte(`{"name":"synthetic-auth.json","name":"synthetic-auth.json","expected_revision":"` + registered.Revision() + `","operation":"set","priority":101}`)
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/priority", bytes.NewReader(body))
	h.PatchAuthFilePriority(ctx)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	current, _ := manager.GetByID(registered.ID)
	if current.Attributes["priority"] != "10" {
		t.Fatalf("duplicate-field request mutated priority to %q", current.Attributes["priority"])
	}
}

func TestPatchAuthFilePriorityPersistsExactUnsetThroughFileStore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	path := filepath.Join(dir, "synthetic-auth.json")
	if err := os.WriteFile(path, []byte(`{"type":"codex","access_token":"synthetic-token","priority":10,"note":"keep","disabled":false}`), 0o600); err != nil {
		t.Fatalf("seed auth file: %v", err)
	}
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(dir)
	manager := coreauth.NewManager(store, &coreauth.FillFirstSelector{}, nil)
	if err := manager.Load(context.Background()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	registered, ok := manager.GetByID("synthetic-auth.json")
	if !ok {
		t.Fatal("GetByID() missing file-backed auth")
	}
	body := []byte(`{"name":"synthetic-auth.json","expected_revision":"` + registered.Revision() + `","operation":"unset"}`)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: dir}, manager)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/priority", bytes.NewReader(body))
	h.PatchAuthFilePriority(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	var persisted map[string]any
	if err = json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("decode auth file: %v", err)
	}
	if _, ok = persisted["priority"]; ok {
		t.Fatalf("priority remains on disk: %s", raw)
	}
	if persisted["access_token"] != "synthetic-token" || persisted["note"] != "keep" || persisted["disabled"] != false {
		t.Fatalf("unrelated fields changed: %s", raw)
	}
	committed, _ := manager.GetByID(registered.ID)
	replayedAuths, err := store.List(context.Background())
	if err != nil || len(replayedAuths) != 1 {
		t.Fatalf("List() after mutation auths=%d error=%v", len(replayedAuths), err)
	}
	replayed, err := manager.Update(coreauth.WithSkipPersist(context.Background()), replayedAuths[0])
	if err != nil {
		t.Fatalf("watcher replay Update() error = %v", err)
	}
	if replayed.Revision() != committed.Revision() {
		t.Fatalf("watcher replay rotated revision from %q to %q", committed.Revision(), replayed.Revision())
	}
}

func TestPatchAuthFilePriorityRejectsSameNameReplacementBeforeWatcherReload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	path := filepath.Join(dir, "synthetic-auth.json")
	if err := os.WriteFile(path, []byte(`{"type":"codex","access_token":"old-synthetic-token","priority":10}`), 0o600); err != nil {
		t.Fatalf("seed auth file: %v", err)
	}
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(dir)
	manager := coreauth.NewManager(store, &coreauth.FillFirstSelector{}, nil)
	if err := manager.Load(context.Background()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	registered, _ := manager.GetByID("synthetic-auth.json")
	replacement := []byte(`{"type":"codex","access_token":"replacement-synthetic-token","priority":7}`)
	if err := os.WriteFile(path, replacement, 0o600); err != nil {
		t.Fatalf("replace auth file: %v", err)
	}
	body := []byte(`{"name":"synthetic-auth.json","expected_revision":"` + registered.Revision() + `","operation":"set","priority":101}`)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: dir}, manager)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/priority", bytes.NewReader(body))
	h.PatchAuthFilePriority(ctx)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusConflict, recorder.Body.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read replacement: %v", err)
	}
	if string(got) != string(replacement) {
		t.Fatalf("replacement changed: got %s want %s", got, replacement)
	}
	current, _ := manager.GetByID(registered.ID)
	if current.Revision() != registered.Revision() || current.Metadata["priority"] != float64(10) {
		t.Fatalf("manager changed after source conflict: revision=%q priority=%#v", current.Revision(), current.Metadata["priority"])
	}
}

func TestPatchAuthFilePriorityReportsWebsocketIncompatibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &managementPriorityStore{}
	manager := coreauth.NewManager(store, &coreauth.FillFirstSelector{}, nil)
	registered, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:         "synthetic-auth.json",
		FileName:   "synthetic-auth.json",
		Provider:   "codex",
		Attributes: map[string]string{"path": "/synthetic/synthetic-auth.json", "priority": "10", "websockets": "true"},
		Metadata:   map[string]any{"type": "codex", "priority": float64(10), "websockets": true},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	body := []byte(`{"name":"synthetic-auth.json","expected_revision":"` + registered.Revision() + `","operation":"set","priority":101}`)
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/priority", bytes.NewReader(body))
	h.PatchAuthFilePriority(ctx)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusUnprocessableEntity, recorder.Body.String())
	}
	var payload struct {
		Code string `json:"code"`
	}
	if err = json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Code != "routing_incompatible" {
		t.Fatalf("code = %q, want routing_incompatible", payload.Code)
	}
	current, _ := manager.GetByID(registered.ID)
	if current.Revision() != registered.Revision() || current.Attributes["priority"] != "10" {
		t.Fatalf("incompatible mutation changed auth: revision=%q priority=%q", current.Revision(), current.Attributes["priority"])
	}
}
