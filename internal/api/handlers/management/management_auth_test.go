package management

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
)

func TestListAuthFiles(t *testing.T) {
	gin.SetMode(gin.TestMode)
	
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	_ = os.MkdirAll(authDir, 0755)
	
	// Create a dummy auth file
	authFile := filepath.Join(authDir, "test.json")
	_ = os.WriteFile(authFile, []byte(`{"access_token": "abc"}`), 0644)
	
	cfg := &config.Config{AuthDir: authDir}
	h, _, cleanup := setupTestHandler(cfg)
	defer cleanup()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	h.ListAuthFiles(c)

	if w.Code != 200 {
		t.Errorf("ListAuthFiles failed: %d, body: %s", w.Code, w.Body.String())
	}
	
	var resp struct {
		Files []any `json:"files"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Files) == 0 {
		t.Errorf("expected at least one auth file, got 0, body: %s", w.Body.String())
	}
}
