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
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	fileauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func listAuthFilesForTest(t *testing.T, h *Handler) []map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	h.ListAuthFiles(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &payload); errDecode != nil {
		t.Fatalf("decode list: %v", errDecode)
	}
	return payload.Files
}

func TestListAuthFiles_ExposesModelWeightsFromRuntimeAndDisk(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "claude-a.json"
	filePath := filepath.Join(authDir, fileName)
	body := `{"type":"claude","email":"a@example.com","weight":5,"model_weights":{"claude-fable-5-1":1,"claude-opus-5":0}}`
	if errWrite := os.WriteFile(filePath, []byte(body), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	// Disk listing (no auth manager) reads the raw file.
	diskFiles := listAuthFilesForTest(t, NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil))
	if len(diskFiles) != 1 {
		t.Fatalf("disk files = %d, want 1", len(diskFiles))
	}
	diskWeights, _ := diskFiles[0]["model_weights"].(map[string]any)
	if diskWeights["claude-fable-5-1"] != float64(1) || diskWeights["claude-opus-5"] != float64(0) {
		t.Fatalf("disk model_weights = %#v", diskFiles[0]["model_weights"])
	}

	// Runtime listing exposes the attribute form the selector consumes.
	manager := coreauth.NewManager(nil, nil, nil)
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:       fileName,
		FileName: fileName,
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path":                         filePath,
			coreauth.AttributeWeight:       "5",
			coreauth.AttributeModelWeights: `{"claude-fable-5-1":1,"claude-opus-5":0}`,
		},
	})
	runtimeFiles := listAuthFilesForTest(t, NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager))
	if len(runtimeFiles) != 1 {
		t.Fatalf("runtime files = %d, want 1", len(runtimeFiles))
	}
	runtimeWeights, _ := runtimeFiles[0]["model_weights"].(map[string]any)
	if runtimeWeights["claude-fable-5-1"] != float64(1) || runtimeWeights["claude-opus-5"] != float64(0) {
		t.Fatalf("runtime model_weights = %#v", runtimeFiles[0]["model_weights"])
	}
	if runtimeFiles[0]["weight"] != float64(5) {
		t.Fatalf("runtime weight = %#v, want 5", runtimeFiles[0]["weight"])
	}

	// Credentials without a table must not gain the key.
	plain := coreauth.NewManager(nil, nil, nil)
	registerAuthForLookupTest(t, plain, &coreauth.Auth{ID: "plain", FileName: "plain.json", Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{coreauth.AttributeWeight: "2"}})
	plainFiles := listAuthFilesForTest(t, NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, plain))
	for _, file := range plainFiles {
		if file["name"] == "plain.json" {
			if _, ok := file["model_weights"]; ok {
				t.Fatalf("plain entry should not expose model_weights: %#v", file)
			}
		}
	}
}

func TestPatchAuthFileFields_ModelWeightsPersistsAndSyncsRuntime(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "claude-mw.json"
	filePath := filepath.Join(authDir, fileName)
	store := fileauth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:         fileName,
		FileName:   fileName,
		Provider:   "claude",
		Attributes: map[string]string{"path": filePath},
		Metadata:   map[string]any{"type": "claude"},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	patch := func(field, value string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		body := `{"name":"` + fileName + `","` + field + `":` + value + `}`
		ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		h.PatchAuthFileFields(ctx)
		return rec
	}
	readPersisted := func() map[string]any {
		t.Helper()
		raw, errRead := os.ReadFile(filePath)
		if errRead != nil {
			t.Fatalf("ReadFile() error = %v", errRead)
		}
		var persisted map[string]any
		if errUnmarshal := json.Unmarshal(raw, &persisted); errUnmarshal != nil {
			t.Fatalf("Unmarshal() error = %v", errUnmarshal)
		}
		return persisted
	}

	if rec := patch("model_weights", `{"Claude-Fable-5-1":1,"claude-opus-5":0}`); rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	updated, ok := manager.GetByID(fileName)
	if !ok || updated.Attributes[coreauth.AttributeModelWeights] != `{"claude-fable-5-1":1,"claude-opus-5":0}` {
		t.Fatalf("runtime model_weights attribute = %#v", updated.Attributes)
	}
	persisted := readPersisted()
	persistedWeights, _ := persisted["model_weights"].(map[string]any)
	if persistedWeights["claude-fable-5-1"] != float64(1) || persistedWeights["claude-opus-5"] != float64(0) {
		t.Fatalf("persisted model_weights = %#v", persisted["model_weights"])
	}

	if rec := patch("model_weights", `{"claude-opus-5":1.5}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("fractional status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if rec := patch("model_weights.claude-opus-5", `3`); rec.Code != http.StatusBadRequest {
		t.Fatalf("nested status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	updated, _ = manager.GetByID(fileName)
	if updated.Attributes[coreauth.AttributeModelWeights] != `{"claude-fable-5-1":1,"claude-opus-5":0}` {
		t.Fatalf("rejected patches must not alter runtime table: %#v", updated.Attributes)
	}

	if rec := patch("model_weights", `null`); rec.Code != http.StatusOK {
		t.Fatalf("reset status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	updated, _ = manager.GetByID(fileName)
	if _, exists := updated.Attributes[coreauth.AttributeModelWeights]; exists {
		t.Fatal("runtime model_weights remains after reset")
	}
	if _, exists := readPersisted()["model_weights"]; exists {
		t.Fatal("persisted model_weights remains after reset")
	}
}
