package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestPutConfigYAMLAcceptsNamedTopLevelAPIKeys(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	initial := "host: \"\"\napi-keys:\n  - existing-key\n"
	if err := os.WriteFile(configPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write initial config: %v", err)
	}

	loaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	h := &Handler{cfg: loaded, configFilePath: configPath}
	body := `host: ""
api-keys:
  - name: primary
    api-key: sk-primary
  - sk-secondary
`

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/config.yaml", strings.NewReader(body))

	h.PutConfigYAML(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	saved, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if !strings.Contains(string(saved), "name: primary") {
		t.Fatalf("saved config missing named api key:\n%s", string(saved))
	}

	reload, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if got, want := len(reload.APIKeys), 2; got != want {
		t.Fatalf("APIKeys length = %d, want %d", got, want)
	}
	if reload.APIKeys[0] != "sk-primary" || reload.APIKeys[1] != "sk-secondary" {
		t.Fatalf("APIKeys = %#v", reload.APIKeys)
	}
}

func TestGetConfigReturnsNamedTopLevelAPIKeys(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	body := `host: ""
api-keys:
  - name: primary
    api-key: sk-primary
  - sk-secondary
`
	if err := os.WriteFile(configPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	loaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	h := &Handler{cfg: loaded, configFilePath: configPath}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/config", nil)

	h.GetConfig(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	jsonBody := recorder.Body.String()
	if !strings.Contains(jsonBody, `"name":"primary"`) {
		t.Fatalf("response missing named api key: %s", jsonBody)
	}
	if !strings.Contains(jsonBody, `"api-key":"sk-primary"`) {
		t.Fatalf("response missing named api key value: %s", jsonBody)
	}
	if !strings.Contains(jsonBody, `"sk-secondary"`) {
		t.Fatalf("response missing plain api key entry: %s", jsonBody)
	}
}
