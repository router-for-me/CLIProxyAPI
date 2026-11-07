package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestStartOAuthV8CodexDeviceFlowIgnoresProviderCompletion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	synctest.Test(t, func(t *testing.T) {
		sessions := newOAuthSessionStore(time.Minute)
		replaceOAuthSessionStoreForTest(t, sessions)
		store := &memoryAuthStore{}
		authenticated := make(chan struct{})
		workerContext := make(chan context.Context, 1)
		h := &Handler{cfg: &config.Config{AuthDir: t.TempDir()}, tokenStore: store}
		h.codexDeviceAuthenticator = codexDeviceAuthenticatorFunc(func(ctx context.Context, _ *config.Config, opts *sdkAuth.LoginOptions) (*coreauth.Auth, error) {
			workerContext <- ctx
			opts.OnDeviceCode(sdkAuth.DeviceCodeInfo{UserCode: "ABCD-EFGH", VerificationURL: "https://auth.openai.com/codex/device", ExpiresIn: 900})
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-authenticated:
				return &coreauth.Auth{ID: "codex-device.json", FileName: "codex-device.json", Provider: "codex"}, nil
			}
		})
		router := gin.New()
		router.GET("/start", h.StartOAuthV8)
		router.GET("/status", h.GetAuthStatus)
		start := httptest.NewRecorder()
		router.ServeHTTP(start, httptest.NewRequest(http.MethodGet, "/start?provider=codex&flow=device", nil))
		ctx := <-workerContext
		defer func() {
			select {
			case <-authenticated:
			default:
				close(authenticated)
			}
			<-ctx.Done()
			synctest.Wait()
		}()
		if start.Code != http.StatusOK {
			t.Fatalf("start status = %d, body=%s", start.Code, start.Body.String())
		}
		var response struct{ State string }
		if err := json.Unmarshal(start.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		RegisterOAuthSession("browser-state", "codex")
		if got := CompleteOAuthSessionsByProvider("codex"); got != 1 {
			t.Errorf("provider completion count = %d, want only the browser session", got)
		}
		if status := performOAuthStatusRequest(t, router, "browser-state"); status.Status != "ok" {
			t.Errorf("browser status = %#v, want ok", status)
		}
		deadline := time.NewTimer(3 * time.Second)
		defer deadline.Stop()
		<-deadline.C
		synctest.Wait()
		if status := performOAuthStatusRequest(t, router, response.State); status.Status != "wait" {
			t.Errorf("device status after unrelated completion = %#v, want wait", status)
		}
		if ctx.Err() != nil {
			t.Error("provider completion cancelled the device authenticator")
		}
		close(authenticated)
		<-ctx.Done()
		synctest.Wait()
		if _, ok := store.items["codex-device.json"]; !ok {
			t.Error("device credentials were not persisted after authentication")
		}
		if status := performOAuthStatusRequest(t, router, response.State); status.Status != "ok" {
			t.Errorf("device status after persistence = %#v, want ok", status)
		}
	})
}
