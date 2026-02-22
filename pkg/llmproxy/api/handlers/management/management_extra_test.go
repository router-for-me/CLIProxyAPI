package management

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/usage"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestNewHandler(t *testing.T) {
	_ = os.Setenv("MANAGEMENT_PASSWORD", "testpass")
	defer func() { _ = os.Unsetenv("MANAGEMENT_PASSWORD") }()
	cfg := &config.Config{}
	h := NewHandler(cfg, "config.yaml", nil)
	if h.envSecret != "testpass" {
		t.Errorf("expected envSecret testpass, got %s", h.envSecret)
	}
	if !h.allowRemoteOverride {
		t.Errorf("expected allowRemoteOverride true")
	}

	h2 := NewHandlerWithoutConfigFilePath(cfg, nil)
	if h2.configFilePath != "" {
		t.Errorf("expected empty configFilePath, got %s", h2.configFilePath)
	}
}

func TestHandler_Setters(t *testing.T) {
	h := &Handler{}
	cfg := &config.Config{Port: 8080}
	h.SetConfig(cfg)
	if h.cfg.Port != 8080 {
		t.Errorf("SetConfig failed")
	}

	h.SetAuthManager(nil)
	stats := &usage.RequestStatistics{}
	h.SetUsageStatistics(stats)
	if h.usageStats != stats {
		t.Errorf("SetUsageStatistics failed")
	}

	h.SetLocalPassword("pass")
	if h.localPassword != "pass" {
		t.Errorf("SetLocalPassword failed")
	}

	tmpDir, _ := os.MkdirTemp("", "logtest")
	defer func() { _ = os.RemoveAll(tmpDir) }()
	h.SetLogDirectory(tmpDir)
	if !filepath.IsAbs(h.logDir) {
		t.Errorf("SetLogDirectory should result in absolute path")
	}
}

func TestMiddleware_RemoteDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.RemoteManagement.AllowRemote = false
	h := &Handler{cfg: cfg, failedAttempts: make(map[string]*attemptInfo)}

	router := gin.New()
	router.Use(h.Middleware())
	router.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestMiddleware_MissingKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.RemoteManagement.AllowRemote = true
	cfg.RemoteManagement.SecretKey = "dummy" // Not empty
	h := &Handler{cfg: cfg, failedAttempts: make(map[string]*attemptInfo)}

	router := gin.New()
	router.Use(h.Middleware())
	router.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "1.2.3.4:1234" // Ensure it's not local
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestMiddleware_Localhost(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.RemoteManagement.SecretKey = "$2a$10$Unused" //bcrypt hash
	h := &Handler{cfg: cfg, envSecret: "envpass", failedAttempts: make(map[string]*attemptInfo)}

	router := gin.New()
	router.Use(h.Middleware())
	router.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

	// Test local access with envSecret
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Management-Key", "envpass")
	req.RemoteAddr = "127.0.0.1:1234"
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestPurgeStaleAttempts(t *testing.T) {
	h := &Handler{
		failedAttempts: make(map[string]*attemptInfo),
	}
	now := time.Now()
	h.failedAttempts["1.1.1.1"] = &attemptInfo{
		lastActivity: now.Add(-3 * time.Hour),
	}
	h.failedAttempts["2.2.2.2"] = &attemptInfo{
		lastActivity: now,
	}
	h.failedAttempts["3.3.3.3"] = &attemptInfo{
		lastActivity: now.Add(-3 * time.Hour),
		blockedUntil: now.Add(1 * time.Hour),
	}

	h.purgeStaleAttempts()

	if _, ok := h.failedAttempts["1.1.1.1"]; ok {
		t.Errorf("1.1.1.1 should have been purged")
	}
	if _, ok := h.failedAttempts["2.2.2.2"]; !ok {
		t.Errorf("2.2.2.2 should not have been purged")
	}
	if _, ok := h.failedAttempts["3.3.3.3"]; !ok {
		t.Errorf("3.3.3.3 should not have been purged (banned)")
	}
}

func TestUpdateFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmpFile, _ := os.CreateTemp("", "config*.yaml")
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	_ = os.WriteFile(tmpFile.Name(), []byte("{}"), 0644)

	cfg := &config.Config{}
	h := &Handler{cfg: cfg, configFilePath: tmpFile.Name()}

	// Test updateBoolField
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/", strings.NewReader(`{"value": true}`))
	var bVal bool
	h.updateBoolField(c, func(v bool) { bVal = v })
	if !bVal {
		t.Errorf("updateBoolField failed")
	}

	// Test updateIntField
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/", strings.NewReader(`{"value": 42}`))
	var iVal int
	h.updateIntField(c, func(v int) { iVal = v })
	if iVal != 42 {
		t.Errorf("updateIntField failed")
	}

	// Test updateStringField
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/", strings.NewReader(`{"value": "hello"}`))
	var sVal string
	h.updateStringField(c, func(v string) { sVal = v })
	if sVal != "hello" {
		t.Errorf("updateStringField failed")
	}
}

func TestGetUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stats := usage.GetRequestStatistics()
	h := &Handler{usageStats: stats}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	h.GetUsageStatistics(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	// Test export
	wExport := httptest.NewRecorder()
	cExport, _ := gin.CreateTestContext(wExport)
	h.ExportUsageStatistics(cExport)
	if wExport.Code != http.StatusOK {
		t.Errorf("export failed")
	}

	// Test import
	wImport := httptest.NewRecorder()
	cImport, _ := gin.CreateTestContext(wImport)
	cImport.Request = httptest.NewRequest("POST", "/", strings.NewReader(wExport.Body.String()))
	h.ImportUsageStatistics(cImport)
	if wImport.Code != http.StatusOK {
		t.Errorf("import failed: %d, body: %s", wImport.Code, wImport.Body.String())
	}
}

func TestGetModels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/?channel=codex", nil)
	h.GetStaticModelDefinitions(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
}

func TestGetQuota(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	h := &Handler{cfg: cfg}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	h.GetSwitchProject(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestGetConfigYAML(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmpFile, _ := os.CreateTemp("", "config*.yaml")
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	_ = os.WriteFile(tmpFile.Name(), []byte("test: true"), 0644)

	h := &Handler{configFilePath: tmpFile.Name()}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	h.GetConfigYAML(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if w.Body.String() != "test: true" {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
}

func TestPutConfigYAML(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmpDir, _ := os.MkdirTemp("", "configtest")
	defer func() { _ = os.RemoveAll(tmpDir) }()
	tmpFile := filepath.Join(tmpDir, "config.yaml")
	_ = os.WriteFile(tmpFile, []byte("debug: false"), 0644)

	h := &Handler{configFilePath: tmpFile, cfg: &config.Config{}}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/", strings.NewReader("debug: true"))

	h.PutConfigYAML(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
}

func TestGetLogs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmpDir, _ := os.MkdirTemp("", "logtest")
	defer func() { _ = os.RemoveAll(tmpDir) }()
	logFile := filepath.Join(tmpDir, "main.log")
	_ = os.WriteFile(logFile, []byte("test log"), 0644)

	cfg := &config.Config{LoggingToFile: true}
	h := &Handler{logDir: tmpDir, cfg: cfg, authManager: coreauth.NewManager(nil, nil, nil)}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	h.GetLogs(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
}

func TestDeleteAuthFile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmpDir, _ := os.MkdirTemp("", "authtest")
	defer func() { _ = os.RemoveAll(tmpDir) }()
	authFile := filepath.Join(tmpDir, "testauth.json")
	_ = os.WriteFile(authFile, []byte("{}"), 0644)

	cfg := &config.Config{AuthDir: tmpDir}
	h := &Handler{cfg: cfg, authManager: coreauth.NewManager(nil, nil, nil)}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("DELETE", "/?name=testauth.json", nil)

	h.DeleteAuthFile(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}

	if _, err := os.Stat(authFile); !os.IsNotExist(err) {
		t.Errorf("file should have been deleted")
	}
}

func TestIsReadOnlyConfigWriteError(t *testing.T) {
	if !isReadOnlyConfigWriteError(&os.PathError{Op: "open", Path: "/tmp/config.yaml", Err: syscall.EROFS}) {
		t.Fatal("expected EROFS path error to be treated as read-only config write error")
	}
	if !isReadOnlyConfigWriteError(errors.New("open /CLIProxyAPI/config.yaml: read-only file system")) {
		t.Fatal("expected read-only file system message to be treated as read-only config write error")
	}
	if isReadOnlyConfigWriteError(errors.New("permission denied")) {
		t.Fatal("did not expect generic permission error to be treated as read-only config write error")
	}
}
