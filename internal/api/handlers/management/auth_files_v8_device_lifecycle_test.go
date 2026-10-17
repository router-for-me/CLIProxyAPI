package management

import (
	"context"
	"encoding/json"
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

func TestStartOAuthV8CodexDeviceFlowCancellationAndSave(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, cancelBeforeSave := range []bool{true, false} {
		name := "during-save"
		if cancelBeforeSave {
			name = "before-save"
		}
		t.Run(name, func(t *testing.T) {
			sessions := newOAuthSessionStore(time.Minute)
			replaceOAuthSessionStoreForTest(t, sessions)
			loginReleased := make(chan struct{})
			saveEntered := make(chan struct{})
			saveReleased := make(chan struct{})
			workerDone := make(chan struct{})
			store := &memoryAuthStore{}
			h := &Handler{cfg: &config.Config{AuthDir: t.TempDir()}, tokenStore: store}
			h.postAuthHook = func(context.Context, *coreauth.Auth) error {
				close(saveEntered)
				<-saveReleased
				return nil
			}
			h.codexDeviceAuthenticator = codexDeviceAuthenticatorFunc(func(ctx context.Context, _ *config.Config, opts *sdkAuth.LoginOptions) (*coreauth.Auth, error) {
				// The handler cancels this context after all save work has finished.
				go func() {
					<-ctx.Done()
					close(workerDone)
				}()
				opts.OnDeviceCode(sdkAuth.DeviceCodeInfo{UserCode: "ABCD-EFGH", VerificationURL: "https://auth.openai.com/codex/device", ExpiresIn: 900})
				<-loginReleased
				return &coreauth.Auth{ID: "codex-device.json", FileName: "codex-device.json", Provider: "codex"}, nil
			})
			router := gin.New()
			router.GET("/start", h.StartOAuthV8)
			router.DELETE("/cancel", h.CancelAuthSession)
			start := httptest.NewRecorder()
			router.ServeHTTP(start, httptest.NewRequest(http.MethodGet, "/start?provider=codex&flow=device", nil))
			if start.Code != http.StatusOK {
				t.Fatalf("start = %d %s", start.Code, start.Body.String())
			}
			var response struct{ State string }
			if err := json.Unmarshal(start.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if !cancelBeforeSave {
				close(loginReleased)
				select {
				case <-saveEntered:
				case <-time.After(3 * time.Second):
					t.Fatal("save hook was not entered")
				}
			}
			cancel := httptest.NewRecorder()
			router.ServeHTTP(cancel, httptest.NewRequest(http.MethodDelete, "/cancel?state="+response.State, nil))
			var cancelled struct{ Cancelled bool }
			if err := json.Unmarshal(cancel.Body.Bytes(), &cancelled); err != nil {
				t.Fatal(err)
			}
			if cancelBeforeSave {
				close(loginReleased)
			}
			close(saveReleased)
			select {
			case <-workerDone:
			case <-time.After(3 * time.Second):
				t.Fatal("device worker did not finish")
			}
			if cancel.Code != http.StatusOK || cancelled.Cancelled != cancelBeforeSave {
				t.Fatalf("cancel = %d %s, want cancelled=%t", cancel.Code, cancel.Body.String(), cancelBeforeSave)
			}
			if _, saved := store.items["codex-device.json"]; saved == cancelBeforeSave {
				t.Fatalf("saved = %t, cancellation succeeded = %t", saved, cancelBeforeSave)
			}
		})
	}
}

func TestStartOAuthV8CodexDeviceFlowRemovesFailedStartupSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sessions := newOAuthSessionStore(time.Minute)
	replaceOAuthSessionStoreForTest(t, sessions)
	h := &Handler{cfg: &config.Config{AuthDir: t.TempDir()}}
	h.codexDeviceAuthenticator = codexDeviceAuthenticatorFunc(func(context.Context, *config.Config, *sdkAuth.LoginOptions) (*coreauth.Auth, error) {
		return nil, errors.New("user-code endpoint unavailable")
	})
	router := gin.New()
	router.GET("/start", h.StartOAuthV8)
	start := httptest.NewRecorder()
	router.ServeHTTP(start, httptest.NewRequest(http.MethodGet, "/start?provider=codex&flow=device", nil))
	if start.Code != http.StatusBadGateway {
		t.Fatalf("start = %d %s", start.Code, start.Body.String())
	}
	sessions.mu.RLock()
	defer sessions.mu.RUnlock()
	if len(sessions.sessions) != 0 {
		t.Fatalf("failed startup retained %d unreachable sessions", len(sessions.sessions))
	}
}
