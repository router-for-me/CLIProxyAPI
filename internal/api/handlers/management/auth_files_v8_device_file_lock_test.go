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
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestStartOAuthV8CodexDeviceFlowBoundsCodexFileLock(t *testing.T) {
	gin.SetMode(gin.TestMode)
	synctest.Test(t, func(t *testing.T) {
		root := t.TempDir()
		authDir := filepath.Join(root, "auths")
		tokenStore := sdkAuth.NewFileTokenStore()
		tokenStore.SetBaseDir(authDir)
		entered := make(chan struct{})
		release := make(chan struct{})
		holderDone := make(chan error, 1)
		go func() {
			holderDone <- codex.WithFileLock(filepath.Join(authDir, "device.json"), func() error {
				close(entered)
				<-release
				return nil
			})
		}()
		releaseHolder := sync.OnceFunc(func() {
			close(release)
			<-holderDone
			synctest.Wait()
		})
		defer releaseHolder()
		<-entered
		sessions := newOAuthSessionStore(time.Minute)
		replaceOAuthSessionStoreForTest(t, sessions)
		workerContext := make(chan context.Context, 1)
		h := &Handler{cfg: &config.Config{AuthDir: authDir}, tokenStore: tokenStore}
		h.codexDeviceAuthenticator = codexDeviceAuthenticatorFunc(func(ctx context.Context, _ *config.Config, opts *sdkAuth.LoginOptions) (*coreauth.Auth, error) {
			workerContext <- ctx
			opts.OnDeviceCode(sdkAuth.DeviceCodeInfo{UserCode: "ABCD-EFGH", VerificationURL: "https://auth.openai.com/codex/device", ExpiresIn: 900})
			return &coreauth.Auth{
				ID: "device.json", FileName: "device.json", Provider: "codex",
				Metadata: map[string]any{"type": "codex", "access_token": "test-access-token"},
			}, nil
		})
		router := gin.New()
		router.GET("/start", h.StartOAuthV8)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/start?provider=codex&flow=device", nil))
		ctx := <-workerContext
		if response.Code != http.StatusOK {
			t.Fatalf("start status=%d, body=%s", response.Code, response.Body.String())
		}
		var result struct{ State string }
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if session, ok := sessions.Get(result.State); !ok || !session.Committing {
			t.Fatalf("device save did not claim the session: %#v, exists=%t", session, ok)
		}
		deadline := time.NewTimer(oauthSessionTTL + time.Second)
		defer deadline.Stop()
		<-deadline.C
		synctest.Wait()
		if session, ok := sessions.Get(result.State); !ok || session.Committing || session.Status != "Failed to save authentication tokens" {
			t.Errorf("blocked Codex file lock did not resolve the device session: %#v, exists=%t", session, ok)
		}
		if ctx.Err() == nil {
			t.Error("device worker kept running past the persistence deadline")
		}
		releaseHolder()
		if session, ok := sessions.Get(result.State); !ok || session.Committing || session.Status != "Failed to save authentication tokens" {
			t.Errorf("releasing the Codex file lock changed the failed session: %#v, exists=%t", session, ok)
		}
		if _, err := os.Stat(filepath.Join(authDir, "device.json")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("device save wrote credentials after the file lock deadline: %v", err)
		}
	})
}
