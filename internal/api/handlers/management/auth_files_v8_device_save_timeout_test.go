package management

import (
	"context"
	"encoding/json"
	"errors"
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

type stalledDeviceSaveStore struct {
	*memoryAuthStore
	wait func(context.Context, *coreauth.Auth) error
}

func (s *stalledDeviceSaveStore) Save(ctx context.Context, auth *coreauth.Auth) (string, error) {
	return "", s.wait(ctx, auth)
}

func TestStartOAuthV8CodexDeviceFlowBoundsPersistence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, phase := range []string{"hook", "store", "post-persist-hook"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sessions := newOAuthSessionStore(time.Minute)
				replaceOAuthSessionStoreForTest(t, sessions)
				entered := make(chan struct{})
				release := make(chan struct{})
				workerContext := make(chan context.Context, 1)
				stall := func(ctx context.Context, _ *coreauth.Auth) error {
					close(entered)
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-release:
						return errors.New("test cleanup")
					}
				}
				h := &Handler{cfg: &config.Config{AuthDir: t.TempDir()}, tokenStore: &memoryAuthStore{}}
				switch phase {
				case "hook":
					h.postAuthHook = stall
				case "store":
					h.tokenStore = &stalledDeviceSaveStore{memoryAuthStore: &memoryAuthStore{}, wait: stall}
				case "post-persist-hook":
					h.postAuthPersistHook = stall
				}
				h.codexDeviceAuthenticator = codexDeviceAuthenticatorFunc(func(ctx context.Context, _ *config.Config, opts *sdkAuth.LoginOptions) (*coreauth.Auth, error) {
					workerContext <- ctx
					opts.OnDeviceCode(sdkAuth.DeviceCodeInfo{UserCode: "ABCD-EFGH", VerificationURL: "https://auth.openai.com/codex/device", ExpiresIn: 900})
					return &coreauth.Auth{ID: "codex-device.json", FileName: "codex-device.json", Provider: "codex"}, nil
				})
				router := gin.New()
				router.GET("/start", h.StartOAuthV8)
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/start?provider=codex&flow=device", nil))
				ctx := <-workerContext
				defer func() {
					close(release)
					<-ctx.Done()
					synctest.Wait()
				}()
				if response.Code != http.StatusOK {
					t.Fatalf("start status=%d, body=%s", response.Code, response.Body.String())
				}
				var result struct{ State string }
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				<-entered
				deadline := time.NewTimer(oauthSessionTTL + time.Second)
				defer deadline.Stop()
				<-deadline.C
				synctest.Wait()
				session, ok := sessions.Get(result.State)
				if !ok || session.Saving || session.Status != "Failed to save authentication tokens" {
					t.Errorf("stalled persistence did not resolve to an error: %#v, exists=%t", session, ok)
				}
				if ctx.Err() == nil {
					t.Error("device worker kept running after the persistence budget")
				}
			})
		})
	}
}
