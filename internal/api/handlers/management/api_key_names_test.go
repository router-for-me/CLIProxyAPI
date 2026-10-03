package management

import (
	"crypto/sha256"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharedKeyNamesPersistenceImportClearAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := []byte("api-keys: [existing-client-key]\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: &config.Config{}, configFilePath: path}
	h.cfg.APIKeys = []string{"existing-client-key"}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(h.cfg.APIKeys[0])))
	request := func(body string, status int) {
		t.Helper()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("PATCH", "/api-key-names", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.PatchAPIKeyNames(c)
		if w.Code != status {
			t.Fatalf("got %d want %d", w.Code, status)
		}
	}
	request(fmt.Sprintf(`{"names":{"%s":" Team ✓ "}}`, fingerprint), 200)
	request(fmt.Sprintf(`{"names":{"%s":"Stale browser"},"only_if_absent":true}`, fingerprint), 200)
	names, err := h.readAPIKeyNamesLocked()
	if err != nil || names[fingerprint] != "Team ✓" {
		t.Fatal("import overwrote shared name")
	}
	request(fmt.Sprintf(`{"names":{"%s":""}}`, fingerprint), 200)
	request(fmt.Sprintf(`{"names":{"%s":"Stale browser"},"only_if_absent":true}`, fingerprint), 200)
	h = &Handler{cfg: h.cfg, configFilePath: path}
	names, err = h.readAPIKeyNamesLocked()
	if err != nil || names[fingerprint] != "" {
		t.Fatal("clear did not persist")
	}
	request(`{"names":{"bad":"invalid"}}`, 400)
	request(fmt.Sprintf(`{"names":{"%s":"existing-client-key"}}`, fingerprint), 400)
	request(fmt.Sprintf(`{"names":{"%s":"bad\nname"}}`, fingerprint), 400)
	request(fmt.Sprintf(`{"names":{"%s":"hidden\u200bname"}}`, fingerprint), 400)
	request(fmt.Sprintf(`{"names":{"%s":"%s"}}`, fingerprint, strings.Repeat("界", 129)), 400)
	saved, _ := os.ReadFile(path)
	if string(saved) != string(original) || len(h.cfg.APIKeys) != 1 {
		t.Fatal("metadata changed key configuration")
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "api-key-names.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	request(fmt.Sprintf(`{"names":{"%s":"New"}}`, fingerprint), 500)
}
