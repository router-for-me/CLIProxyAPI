package management

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
