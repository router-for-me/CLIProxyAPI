package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestStartOAuthV8CodexDeviceFlowRejectsCallbacks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	synctest.Test(t, func(t *testing.T) {
		sessions := newOAuthSessionStore(time.Minute)
		replaceOAuthSessionStoreForTest(t, sessions)
		authDir := t.TempDir()
		workerContext := make(chan context.Context, 1)
		h := &Handler{cfg: &config.Config{AuthDir: authDir}}
		h.codexDeviceAuthenticator = codexDeviceAuthenticatorFunc(func(ctx context.Context, _ *config.Config, opts *sdkAuth.LoginOptions) (*coreauth.Auth, error) {
			workerContext <- ctx
			opts.OnDeviceCode(sdkAuth.DeviceCodeInfo{UserCode: "ABCD-EFGH", VerificationURL: "https://auth.openai.com/codex/device", ExpiresIn: 900})
			<-ctx.Done()
			return nil, ctx.Err()
		})
		router := gin.New()
		router.GET("/start", h.StartOAuthV8)
		router.GET("/v8/management/oauth/callback", h.GetOAuthCallbackV8)
		router.POST("/v8/management/oauth/callback", h.PostOAuthCallbackV8)
		start := httptest.NewRecorder()
		router.ServeHTTP(start, httptest.NewRequest(http.MethodGet, "/start?provider=codex&flow=device", nil))
		if start.Code != http.StatusOK {
			t.Fatalf("start status = %d, body=%s", start.Code, start.Body.String())
		}
		var response struct{ State string }
		if err := json.Unmarshal(start.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		ctx := <-workerContext
		defer func() {
			CancelOAuthSession(response.State)
			<-ctx.Done()
			synctest.Wait()
		}()
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			request := httptest.NewRequest(method, "/v8/management/oauth/callback?state="+response.State+"&code=browser-code", nil)
			if method == http.MethodPost {
				request = httptest.NewRequest(method, "/v8/management/oauth/callback", strings.NewReader(`{"state":"`+response.State+`","code":"browser-code"}`))
				request.Header.Set("Content-Type", "application/json")
			}
			callback := httptest.NewRecorder()
			router.ServeHTTP(callback, request)
			if callback.Code != http.StatusConflict {
				t.Errorf("%s callback status = %d, body=%s", method, callback.Code, callback.Body.String())
			}
		}
		if _, err := WriteOAuthCallbackFileForPendingSession(authDir, "codex", response.State, "browser-code", ""); err == nil {
			t.Error("callback file sink accepted a device-only state")
		}
		if _, err := os.Stat(filepath.Join(authDir, ".oauth-codex-"+response.State+".oauth")); !os.IsNotExist(err) {
			t.Errorf("device callback left an artifact, stat error = %v", err)
		}
		if !IsOAuthSessionPending(response.State, "codex") || ctx.Err() != nil {
			t.Error("rejected callback stopped the pending device flow")
		}
	})
}
