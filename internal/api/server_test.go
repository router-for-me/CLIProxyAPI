package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
)

func TestNewServer(t *testing.T) {
	cfg := &config.Config{
		Port: 8080,
		Debug: true,
	}
	authManager := auth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()
	
	s := NewServer(cfg, authManager, accessManager, "config.yaml")
	if s == nil {
		t.Fatal("NewServer returned nil")
	}
	
	if s.engine == nil {
		t.Error("engine is nil")
	}
	
	if s.handlers == nil {
		t.Error("handlers is nil")
	}
}

func TestServer_RootEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Debug: true}
	s := NewServer(cfg, nil, nil, "config.yaml")
	
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/", nil)
	s.engine.ServeHTTP(w, req)
	
	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestWithMiddleware(t *testing.T) {
	called := false
	mw := func(c *gin.Context) {
		called = true
		c.Next()
	}
	
	cfg := &config.Config{Debug: true}
	s := NewServer(cfg, nil, nil, "config.yaml", WithMiddleware(mw))
	
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/", nil)
	s.engine.ServeHTTP(w, req)
	
	if !called {
		t.Error("extra middleware was not called")
	}
}

func TestWithKeepAliveEndpoint(t *testing.T) {
	onTimeout := func() {
	}
	
	cfg := &config.Config{Debug: true}
	s := NewServer(cfg, nil, nil, "config.yaml", WithKeepAliveEndpoint(100*time.Millisecond, onTimeout))
	
	if !s.keepAliveEnabled {
		t.Error("keep-alive should be enabled")
	}
	
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/keep-alive", nil)
	s.engine.ServeHTTP(w, req)
	
	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	s.Stop(context.Background())
}
