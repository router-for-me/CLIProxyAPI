package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"golang.org/x/crypto/bcrypt"
)

func TestStateMiddleware_DoesNotBlockConcurrentSetConfigForGetRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("generate hash: %v", err)
	}

	h := NewHandler(&config.Config{RemoteManagement: config.RemoteManagement{SecretKey: string(hash)}}, filepath.Join(t.TempDir(), "config.yaml"), nil)
	r := gin.New()
	release := make(chan struct{})
	reached := make(chan struct{})
	r.GET("/guarded", h.StateMiddleware(), func(c *gin.Context) {
		close(reached)
		<-release
		c.Status(http.StatusNoContent)
	})

	go func() {
		req := httptest.NewRequest(http.MethodGet, "/guarded", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	}()

	select {
	case <-reached:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not enter guarded handler")
	}

	updated := make(chan struct{})
	go func() {
		h.SetConfig(&config.Config{})
		close(updated)
	}()

	select {
	case <-updated:
	case <-time.After(150 * time.Millisecond):
		t.Fatal("SetConfig should not wait for in-flight GET management request")
	}

	close(release)

}

func TestStateMiddleware_BlocksConcurrentSetConfigForMutatingRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("generate hash: %v", err)
	}

	h := NewHandler(&config.Config{RemoteManagement: config.RemoteManagement{SecretKey: string(hash)}}, filepath.Join(t.TempDir(), "config.yaml"), nil)
	r := gin.New()
	release := make(chan struct{})
	reached := make(chan struct{})
	r.PUT("/guarded", h.StateMiddleware(), func(c *gin.Context) {
		close(reached)
		<-release
		c.Status(http.StatusNoContent)
	})

	go func() {
		req := httptest.NewRequest(http.MethodPut, "/guarded", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	}()

	select {
	case <-reached:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not enter guarded handler")
	}

	updated := make(chan struct{})
	go func() {
		h.SetConfig(&config.Config{})
		close(updated)
	}()

	select {
	case <-updated:
		t.Fatal("SetConfig should wait for in-flight mutating management request")
	case <-time.After(150 * time.Millisecond):
	}

	close(release)

	select {
	case <-updated:
	case <-time.After(2 * time.Second):
		t.Fatal("SetConfig did not complete after request finished")
	}
}

func TestPutLogsMaxTotalSizeMB_RejectsOversizedValue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("logging-to-file: false\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	h := NewHandler(&config.Config{}, configPath, nil)
	r := gin.New()
	r.PUT("/logs-max-total-size-mb", h.StateMiddleware(), h.PutLogsMaxTotalSizeMB)

	body := strings.NewReader(`{"value":1048577}`)
	req := httptest.NewRequest(http.MethodPut, "/logs-max-total-size-mb", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status: got %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !strings.Contains(resp["error"], "exceeds allowed maximum") {
		t.Fatalf("unexpected error response: %v", resp)
	}
}
