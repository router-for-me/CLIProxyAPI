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
	"github.com/go-git/go-git/v6"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type stalledDeviceSaveStore struct {
	*memoryAuthStore
	wait func(context.Context, *coreauth.Auth) error
}

type blockedDeviceTokenStorage struct {
	entered chan struct{}
	release chan struct{}
}

func (s *blockedDeviceTokenStorage) SaveTokenToFile(string) error {
	close(s.entered)
	<-s.release
	return errors.New("test holder released")
}

func TestStartOAuthV8CodexDeviceFlowBoundsGitStoreLock(t *testing.T) {
	gin.SetMode(gin.TestMode)
	synctest.Test(t, func(t *testing.T) {
		root := t.TempDir()
		remoteDir := filepath.Join(root, "remote.git")
		if _, err := git.PlainInit(remoteDir, true); err != nil {
			t.Fatal(err)
		}
		gitStore := store.NewGitTokenStore(remoteDir, "", "", "")
		authDir := filepath.Join(root, "workspace", "auths")
		gitStore.SetBaseDir(authDir)
		if err := gitStore.EnsureRepository(); err != nil {
			t.Fatal(err)
		}
		blocker := &blockedDeviceTokenStorage{entered: make(chan struct{}), release: make(chan struct{})}
		holderDone := make(chan error, 1)
		go func() {
			_, err := gitStore.Save(context.Background(), &coreauth.Auth{
				ID: "holder.json", FileName: "holder.json", Provider: "claude", Storage: blocker,
			})
			holderDone <- err
		}()
		releaseHolder := sync.OnceFunc(func() {
			close(blocker.release)
			<-holderDone
			synctest.Wait()
		})
		defer releaseHolder()
		<-blocker.entered
		sessions := newOAuthSessionStore(time.Minute)
		replaceOAuthSessionStoreForTest(t, sessions)
		workerContext := make(chan context.Context, 1)
		h := &Handler{cfg: &config.Config{AuthDir: authDir}, tokenStore: gitStore}
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
			t.Errorf("blocked Git store did not resolve the device session: %#v, exists=%t", session, ok)
		}
		if ctx.Err() == nil {
			t.Error("device worker kept running past the persistence deadline")
		}
		releaseHolder()
		if session, ok := sessions.Get(result.State); !ok || session.Committing || session.Status != "Failed to save authentication tokens" {
			t.Errorf("releasing the Git store changed the failed session: %#v, exists=%t", session, ok)
		}
		if _, err := os.Stat(filepath.Join(authDir, "device.json")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("device save wrote credentials despite the blocked store: %v", err)
		}
	})
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
				if !ok || session.Committing || session.Status != "Failed to save authentication tokens" {
					t.Errorf("stalled persistence did not resolve to an error: %#v, exists=%t", session, ok)
				}
				if ctx.Err() == nil {
					t.Error("device worker kept running after the persistence budget")
				}
			})
		})
	}
}
