package management

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestStartOAuthV8CodexDeviceFlowCompletesBeforeRuntimeSync(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, hookFails := range []bool{false, true} {
		name := "success"
		if hookFails {
			name = "error"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sessions := newOAuthSessionStore(2 * oauthSessionTTL)
				replaceOAuthSessionStoreForTest(t, sessions)
				authDir := t.TempDir()
				h := &Handler{cfg: &config.Config{AuthDir: authDir}, tokenStore: sdkAuth.NewFileTokenStore()}
				entered := make(chan struct{})
				release := make(chan struct{})
				finished := make(chan struct{})
				workerContext := make(chan context.Context, 1)
				releaseHook := sync.OnceFunc(func() { close(release) })
				defer releaseHook()
				h.postAuthPersistHook = func(ctx context.Context, record *coreauth.Auth) error {
					defer close(finished)
					close(entered)
					// Runtime registration deliberately outlives the acquisition context.
					<-release
					if hookFails {
						return errors.New("runtime registration failed")
					}
					return nil
				}
				h.codexDeviceAuthenticator = codexDeviceAuthenticatorFunc(func(ctx context.Context, _ *config.Config, opts *sdkAuth.LoginOptions) (*coreauth.Auth, error) {
					workerContext <- ctx
					opts.OnDeviceCode(sdkAuth.DeviceCodeInfo{UserCode: "ABCD-EFGH", VerificationURL: "https://auth.openai.com/codex/device", ExpiresIn: 900})
					return &coreauth.Auth{ID: "device.json", FileName: "device.json", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "test-token"}}, nil
				})
				router := gin.New()
				router.GET("/start", h.StartOAuthV8)
				router.GET("/status", h.GetAuthStatus)
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/start?provider=codex&flow=device", nil))
				ctx := <-workerContext
				if response.Code != http.StatusOK {
					t.Fatalf("start status=%d: %s", response.Code, response.Body.String())
				}
				var result struct{ State string }
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				<-entered
				defer func() { releaseHook(); <-finished; synctest.Wait() }()
				synctest.Wait()
				check := func(stage string) {
					t.Helper()
					session, ok := sessions.Get(result.State)
					if !ok || !session.Completed || session.Committing || session.Status != "" {
						t.Errorf("%s: persisted login held by runtime sync: %#v, exists=%t", stage, session, ok)
					}
					status := httptest.NewRecorder()
					router.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/status?state="+result.State, nil))
					if status.Code != http.StatusOK || status.Body.String() != `{"status":"ok"}` {
						t.Errorf("%s: status=%d %s", stage, status.Code, status.Body.String())
					}
				}
				if _, err := os.Stat(filepath.Join(authDir, "device.json")); err != nil {
					t.Fatalf("credential not persisted before sync: %v", err)
				}
				check("runtime blocked")
				deadline := time.NewTimer(oauthSessionTTL + time.Second)
				defer deadline.Stop()
				<-deadline.C
				synctest.Wait()
				checkExpired := func(stage string) {
					t.Helper()
					if session, ok := sessions.Get(result.State); ok {
						t.Errorf("%s: completed session did not expire while runtime was blocked: %#v", stage, session)
					}
				}
				checkExpired("past acquisition deadline")
				releaseHook()
				<-finished
				<-ctx.Done()
				synctest.Wait()
				checkExpired("runtime finished")
			})
		})
	}
}
