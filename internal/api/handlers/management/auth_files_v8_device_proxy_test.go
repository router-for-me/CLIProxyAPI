package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestStartOAuthV8CodexDeviceFlowUsesAndPersistsSessionProxy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	synctest.Test(t, func(t *testing.T) {
		const sessionProxy = "http://127.0.0.1:18082"
		const globalProxy = "http://127.0.0.1:18081"
		sessions := newOAuthSessionStore(time.Minute)
		replaceOAuthSessionStoreForTest(t, sessions)
		store := &memoryAuthStore{}
		cfg := &config.Config{AuthDir: t.TempDir(), SDKConfig: config.SDKConfig{ProxyURL: globalProxy}}
		h := &Handler{cfg: cfg, tokenStore: store}
		authenticated := make(chan struct{})
		workerContext := make(chan context.Context, 1)
		loginConfig := make(chan *config.Config, 1)
		h.codexDeviceAuthenticator = codexDeviceAuthenticatorFunc(func(ctx context.Context, snapshot *config.Config, opts *sdkAuth.LoginOptions) (*coreauth.Auth, error) {
			workerContext <- ctx
			loginConfig <- snapshot
			opts.OnDeviceCode(sdkAuth.DeviceCodeInfo{UserCode: "ABCD-EFGH", VerificationURL: "https://auth.openai.com/codex/device", ExpiresIn: 900})
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-authenticated:
				return &coreauth.Auth{ID: "codex-device.json", FileName: "codex-device.json", Provider: "codex"}, nil
			}
		})
		router := gin.New()
		router.POST("/start", h.StartOAuthV8)
		router.GET("/status", h.GetAuthStatus)
		start := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/start?flow=device", strings.NewReader(`{"provider":"codex","proxy_url":"`+sessionProxy+`"}`))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(start, request)
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
		if got := <-loginConfig; got == cfg || got.ProxyURL != sessionProxy {
			t.Errorf("authenticator did not receive an isolated session proxy config: same=%t proxy=%q", got == cfg, got.ProxyURL)
		}
		if cfg.ProxyURL != globalProxy {
			t.Errorf("session proxy changed the shared config to %q", cfg.ProxyURL)
		}
		if got := GetOAuthSessionProxyURL(response.State); got != sessionProxy {
			t.Errorf("pending session proxy = %q, want %q", got, sessionProxy)
		}
		close(authenticated)
		<-ctx.Done()
		synctest.Wait()
		if status := performOAuthStatusRequest(t, router, response.State); status.Status != "ok" {
			t.Fatalf("device status after persistence = %#v, want ok", status)
		}
		record := store.items["codex-device.json"]
		if record == nil || record.ProxyURL != sessionProxy || record.Metadata["proxy_url"] != sessionProxy {
			t.Errorf("persisted device credentials lost the session proxy: %#v", record)
		}
		if got := GetOAuthSessionProxyURL(response.State); got != "" {
			t.Errorf("completed session retained proxy %q", got)
		}
	})
}
