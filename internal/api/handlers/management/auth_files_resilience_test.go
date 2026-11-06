package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestListAuthFiles_IsolatesEntryMarshalFailure(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileNameHealthy := "healthy.json"
	filePathHealthy := filepath.Join(authDir, fileNameHealthy)
	if errWrite := os.WriteFile(filePathHealthy, []byte(`{"type":"gemini"}`), 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}

	fileNameBroken := "broken.json"
	filePathBroken := filepath.Join(authDir, fileNameBroken)
	if errWrite := os.WriteFile(filePathBroken, []byte(`{"type":"antigravity"}`), 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:       "auth-healthy",
		Index:    "idx-healthy",
		FileName: fileNameHealthy,
		Provider: "gemini",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path": filePathHealthy,
		},
	})

	// An auth entry with unmarshalable channel metadata that would panic or fail json.Marshal
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:       "auth-broken",
		Index:    "idx-broken",
		FileName: fileNameBroken,
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path": filePathBroken,
		},
		Metadata: map[string]any{
			"quota_probe": map[string]any{
				"unmarshalable_channel": make(chan int),
			},
		},
	})

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)

	h.ListAuthFiles(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &payload); errDecode != nil {
		t.Fatalf("failed to decode response JSON: %v", errDecode)
	}

	if len(payload.Files) != 2 {
		t.Fatalf("expected 2 files, got %d (body: %s)", len(payload.Files), rec.Body.String())
	}

	var foundBroken, foundHealthy bool
	for _, f := range payload.Files {
		if f["id"] == "auth-broken" {
			foundBroken = true
			if f["status"] != string(coreauth.StatusActive) {
				t.Errorf("expected degraded entry to preserve status, got %v", f["status"])
			}
		}
		if f["id"] == "auth-healthy" {
			foundHealthy = true
		}
	}

	if !foundHealthy || !foundBroken {
		t.Fatalf("missing expected entries in response: healthy=%v, broken=%v", foundHealthy, foundBroken)
	}
}
