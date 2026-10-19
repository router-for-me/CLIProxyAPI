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

func TestStartOAuthV8CodexDeviceFlowCancelsDisconnectedStartup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sessions := newOAuthSessionStore(time.Minute)
	replaceOAuthSessionStoreForTest(t, sessions)
	requestCtx, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	entered := make(chan struct{})
	stopped := make(chan struct{})
	release := make(chan struct{})
	h := &Handler{cfg: &config.Config{AuthDir: t.TempDir()}}
	h.codexDeviceAuthenticator = codexDeviceAuthenticatorFunc(func(ctx context.Context, _ *config.Config, _ *sdkAuth.LoginOptions) (*coreauth.Auth, error) {
		defer close(stopped)
		close(entered)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return nil, errors.New("test cleanup")
		}
	})
	router := gin.New()
	router.GET("/start", h.StartOAuthV8)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/start?provider=codex&flow=device", nil).WithContext(requestCtx))
	}()
	t.Cleanup(func() {
		close(release)
		<-done
		<-stopped
	})
	<-entered
	disconnect()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("startup ignored the disconnected request")
	}
	<-stopped
	if response.Body.Len() != 0 {
		t.Fatalf("disconnected startup wrote a response: %s", response.Body.String())
	}
	sessions.mu.RLock()
	defer sessions.mu.RUnlock()
	if len(sessions.sessions) != 0 {
		t.Fatalf("disconnected startup retained %d unreachable sessions", len(sessions.sessions))
	}
}

func TestStartOAuthV8CodexDeviceFlowContinuesAfterResponseRequestEnds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sessions := newOAuthSessionStore(time.Minute)
	replaceOAuthSessionStoreForTest(t, sessions)
	requestCtx, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	release := make(chan struct{})
	workerContext := make(chan context.Context, 1)
	h := &Handler{cfg: &config.Config{AuthDir: t.TempDir()}, tokenStore: &memoryAuthStore{}}
	h.codexDeviceAuthenticator = codexDeviceAuthenticatorFunc(func(ctx context.Context, _ *config.Config, opts *sdkAuth.LoginOptions) (*coreauth.Auth, error) {
		workerContext <- ctx
		opts.OnDeviceCode(sdkAuth.DeviceCodeInfo{UserCode: "ABCD-EFGH", VerificationURL: "https://auth.openai.com/codex/device", ExpiresIn: 900})
		<-release
		return &coreauth.Auth{ID: "codex-device.json", FileName: "codex-device.json", Provider: "codex"}, ctx.Err()
	})
	router := gin.New()
	router.GET("/start", h.StartOAuthV8)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/start?provider=codex&flow=device", nil).WithContext(requestCtx))
	ctx := <-workerContext
	disconnect()
	close(release)
	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("device worker did not finish")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("start = %d %s", response.Code, response.Body.String())
	}
	var result struct{ State string }
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if session, ok := sessions.Get(result.State); !ok || !session.Completed {
		t.Fatalf("request ending after a successful response prevented login completion: %#v", session)
	}
}

type failedDeviceStartResponse struct{ *httptest.ResponseRecorder }

func (w failedDeviceStartResponse) Write([]byte) (int, error) {
	return 0, errors.New("connection closed")
}

type failedDeviceStartFlushResponse struct{ *httptest.ResponseRecorder }

func (w failedDeviceStartFlushResponse) FlushError() error {
	return errors.New("connection closed during flush")
}

func TestStartOAuthV8CodexDeviceFlowDiscardsFailedResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, failure := range []string{"write", "flush"} {
		for _, completeLogin := range []bool{false, true} {
			name := "polling"
			if completeLogin {
				name = "authenticated"
			}
			t.Run(failure+"/"+name, func(t *testing.T) {
				sessions := newOAuthSessionStore(time.Minute)
				replaceOAuthSessionStoreForTest(t, sessions)
				release := make(chan struct{})
				workerContext := make(chan context.Context, 1)
				store := &memoryAuthStore{}
				h := &Handler{cfg: &config.Config{AuthDir: t.TempDir()}, tokenStore: store}
				h.codexDeviceAuthenticator = codexDeviceAuthenticatorFunc(func(ctx context.Context, _ *config.Config, opts *sdkAuth.LoginOptions) (*coreauth.Auth, error) {
					workerContext <- ctx
					opts.OnDeviceCode(sdkAuth.DeviceCodeInfo{UserCode: "ABCD-EFGH", VerificationURL: "https://auth.openai.com/codex/device", ExpiresIn: 900})
					if completeLogin {
						return &coreauth.Auth{ID: "codex-device.json", FileName: "codex-device.json", Provider: "codex"}, nil
					}
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-release:
						return nil, errors.New("test cleanup")
					}
				})
				router := gin.New()
				router.GET("/start", h.StartOAuthV8)
				var writer http.ResponseWriter = failedDeviceStartResponse{httptest.NewRecorder()}
				if failure == "flush" {
					writer = failedDeviceStartFlushResponse{httptest.NewRecorder()}
				}
				router.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/start?provider=codex&flow=device", nil))
				ctx := <-workerContext
				t.Cleanup(func() {
					close(release)
					<-ctx.Done()
				})
				select {
				case <-ctx.Done():
				case <-time.After(3 * time.Second):
					t.Fatal("device worker kept polling after the response failed")
				}
				if len(store.items) != 0 {
					t.Fatal("device auth was saved despite the failed response")
				}
				sessions.mu.RLock()
				defer sessions.mu.RUnlock()
				if len(sessions.sessions) != 0 {
					t.Fatalf("failed response retained %d unreachable sessions", len(sessions.sessions))
				}
			})
		}
	}
}
