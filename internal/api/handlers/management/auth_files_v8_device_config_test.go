package management

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestStartOAuthV8CodexDeviceFlowDoesNotRaceConfigReload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	replaceOAuthSessionStoreForTest(t, newOAuthSessionStore(time.Minute))
	cfg := &config.Config{AuthDir: t.TempDir()}
	h := &Handler{cfg: cfg}
	h.codexDeviceAuthenticator = codexDeviceAuthenticatorFunc(func(_ context.Context, snapshot *config.Config, _ *sdkAuth.LoginOptions) (*coreauth.Auth, error) {
		if snapshot == nil || snapshot.AuthDir != cfg.AuthDir {
			t.Errorf("device login received invalid config: %#v", snapshot)
		}
		return nil, errors.New("test stops before provider login")
	})
	router := gin.New()
	router.GET("/start", h.StartOAuthV8)
	for range 8 {
		start := make(chan struct{})
		reloaded := make(chan struct{})
		go func() {
			defer close(reloaded)
			<-start
			h.SetConfig(cfg)
		}()
		close(start)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/start?provider=codex&flow=device", nil))
		<-reloaded
		if response.Code != http.StatusBadGateway {
			t.Fatalf("failed startup status = %d, body=%s", response.Code, response.Body.String())
		}
	}
}

func TestStartOAuthV8CodexDeviceFlowKeepsConfigAcrossProxyUpdate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	replaceOAuthSessionStoreForTest(t, newOAuthSessionStore(time.Minute))
	cfg := &config.Config{AuthDir: t.TempDir()}
	cfg.ProxyURL = "http://original.example.test:8080"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("auth-dir: "+cfg.AuthDir+"\nproxy-url: "+cfg.ProxyURL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(cfg, path, nil)
	proxyUpdated := make(chan struct{})
	workerContext := make(chan context.Context, 1)
	h.codexDeviceAuthenticator = codexDeviceAuthenticatorFunc(func(ctx context.Context, snapshot *config.Config, opts *sdkAuth.LoginOptions) (*coreauth.Auth, error) {
		workerContext <- ctx
		opts.OnDeviceCode(sdkAuth.DeviceCodeInfo{UserCode: "ABCD-EFGH", VerificationURL: "https://auth.openai.com/codex/device", ExpiresIn: 900})
		<-proxyUpdated
		if snapshot.ProxyURL != cfg.ProxyURL {
			t.Errorf("login proxy changed during authorization: got %q, want %q", snapshot.ProxyURL, cfg.ProxyURL)
		}
		return nil, errors.New("test stops before provider login")
	})
	router := gin.New()
	router.GET("/start", h.StartOAuthV8)
	router.PUT("/v0/management/proxy-url", h.PutProxyURL)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/start?provider=codex&flow=device", nil))
	ctx := <-workerContext
	t.Cleanup(func() {
		close(proxyUpdated)
		<-ctx.Done()
	})
	if response.Code != http.StatusOK {
		t.Fatalf("start status = %d, body=%s", response.Code, response.Body.String())
	}
	updated := httptest.NewRecorder()
	router.ServeHTTP(updated, httptest.NewRequest(http.MethodPut, "/v0/management/proxy-url", strings.NewReader(`{"value":"http://updated.example.test:8080"}`)))
	if updated.Code != http.StatusOK {
		t.Fatalf("proxy update status = %d, body=%s", updated.Code, updated.Body.String())
	}
}
