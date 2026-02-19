package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestGetConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Debug: true}
	h := &Handler{cfg: cfg}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	h.GetConfig(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var got config.Config
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if !got.Debug {
		t.Errorf("expected debug true, got false")
	}
}

func TestGetLatestVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{}
	_ = h
}

func TestPutStringList(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	h := &Handler{cfg: cfg}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/", strings.NewReader(`["a", "b"]`))

	var list []string
	set := func(arr []string) { list = arr }
	h.putStringList(c, set, nil)

	if len(list) != 2 || list[0] != "a" || list[1] != "b" {
		t.Errorf("unexpected list: %v", list)
	}
}

func TestGetDebug(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Debug: true}
	h := &Handler{cfg: cfg}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	h.GetDebug(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	var got struct {
		Debug bool `json:"debug"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if !got.Debug {
		t.Errorf("expected debug true, got false")
	}
}

func TestPutDebug(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmpFile, _ := os.CreateTemp("", "config*.yaml")
	defer os.Remove(tmpFile.Name())
	_, _ = tmpFile.Write([]byte("{}"))
	_ = tmpFile.Close()

	cfg := &config.Config{Debug: false}
	h := &Handler{cfg: cfg, configFilePath: tmpFile.Name()}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/", strings.NewReader(`{"value": true}`))

	h.PutDebug(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d, body: %s", w.Code, w.Body.String())
	}

	if !cfg.Debug {
		t.Errorf("expected debug true, got false")
	}
}
