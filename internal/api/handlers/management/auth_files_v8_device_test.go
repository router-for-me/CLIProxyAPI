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

type codexDeviceAuthenticatorFunc func(context.Context, *config.Config, *sdkAuth.LoginOptions) (*coreauth.Auth, error)

func (f codexDeviceAuthenticatorFunc) Provider() string            { return "codex" }
func (f codexDeviceAuthenticatorFunc) RefreshLead() *time.Duration { return nil }
func (f codexDeviceAuthenticatorFunc) Login(ctx context.Context, cfg *config.Config, opts *sdkAuth.LoginOptions) (*coreauth.Auth, error) {
	return f(ctx, cfg, opts)
}

func TestStartOAuthV8CodexDeviceFlowReturnsCodeAndUsesSessionStatusAndCancel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	synctest.Test(t, func(t *testing.T) {
		cancelled := make(chan struct{})
		h := &Handler{cfg: &config.Config{AuthDir: t.TempDir()}}
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		h.codexDeviceAuthenticator = codexDeviceAuthenticatorFunc(func(ctx context.Context, _ *config.Config, opts *sdkAuth.LoginOptions) (*coreauth.Auth, error) {
			opts.OnDeviceCode(sdkAuth.DeviceCodeInfo{UserCode: "ABCD-EFGH", VerificationURL: "https://auth.openai.com/codex/device", ExpiresIn: 900})
			select {
			case <-ctx.Done():
				close(cancelled)
			case <-release:
			}

			return nil, ctx.Err()
		})
		router := gin.New()
		router.GET("/v8/management/oauth/auth-url", h.StartOAuthV8)
		router.GET("/v8/management/oauth/status", h.GetAuthStatus)
		router.DELETE("/v8/management/oauth/session", h.CancelAuthSession)

		start := httptest.NewRecorder()
		router.ServeHTTP(start, httptest.NewRequest(http.MethodGet, "/v8/management/oauth/auth-url?provider=codex&flow=device", nil))
		if start.Code != http.StatusOK {
			t.Fatalf("start status = %d, body=%s", start.Code, start.Body.String())
		}
		var response map[string]any
		if err := json.Unmarshal(start.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response["flow"] != "device" || response["user_code"] != "ABCD-EFGH" || response["url"] != "https://auth.openai.com/codex/device" {
			t.Fatalf("device login response = %s", start.Body.String())
		}
		state, _ := response["state"].(string)
		if state == "" {
			t.Fatalf("missing state: %s", start.Body.String())
		}
		for _, secret := range []string{"private-device-auth-id", "access_token", "refresh_token", "authorization_code", "code_verifier"} {
			if strings.Contains(start.Body.String(), secret) {
				t.Fatalf("response leaked %q: %s", secret, start.Body.String())
			}
		}
		status := httptest.NewRecorder()
		router.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/v8/management/oauth/status?state="+state, nil))
		if status.Code != http.StatusOK || strings.TrimSpace(status.Body.String()) != `{"status":"wait"}` {
			t.Fatalf("pending status = %d %s", status.Code, status.Body.String())
		}

		cancel := httptest.NewRecorder()
		router.ServeHTTP(cancel, httptest.NewRequest(http.MethodDelete, "/v8/management/oauth/session?state="+state, nil))
		if cancel.Code != http.StatusOK {
			t.Fatalf("cancel status = %d, body=%s", cancel.Code, cancel.Body.String())
		}
		select {
		case <-cancelled:
		case <-time.After(3 * time.Second):
			t.Fatal("device login context was not cancelled")
		}
	})
}

func TestStartOAuthV8CodexDeviceFlowSavesThroughManagementAuthHook(t *testing.T) {
	gin.SetMode(gin.TestMode)
	saved := make(chan string, 1)
	workerContext := make(chan context.Context, 1)
	preSave := make(chan string, 1)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	store := &memoryAuthStore{}
	h.tokenStore = store
	h.SetPostAuthHook(func(_ context.Context, record *coreauth.Auth) error {
		preSave <- record.ID
		return nil
	})
	h.SetPostAuthPersistHook(func(_ context.Context, record *coreauth.Auth) error {
		saved <- record.ID
		return nil
	})
	h.codexDeviceAuthenticator = codexDeviceAuthenticatorFunc(func(ctx context.Context, _ *config.Config, opts *sdkAuth.LoginOptions) (*coreauth.Auth, error) {
		workerContext <- ctx
		opts.OnDeviceCode(sdkAuth.DeviceCodeInfo{UserCode: "ABCD-EFGH", VerificationURL: "https://auth.openai.com/codex/device", ExpiresIn: 900})
		return &coreauth.Auth{ID: "codex-device.json", FileName: "codex-device.json", Provider: "codex"}, nil
	})
	router := gin.New()
	router.GET("/v8/management/oauth/auth-url", h.StartOAuthV8)
	router.GET("/v8/management/oauth/status", h.GetAuthStatus)

	start := httptest.NewRecorder()
	router.ServeHTTP(start, httptest.NewRequest(http.MethodGet, "/v8/management/oauth/auth-url?provider=codex&flow=device", nil))
	ctx := <-workerContext
	t.Cleanup(func() {
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
			t.Error("device worker did not finish")
		}
	})
	if start.Code != http.StatusOK {
		t.Fatalf("start status = %d, body=%s", start.Code, start.Body.String())
	}
	var startResponse map[string]any
	if err := json.Unmarshal(start.Body.Bytes(), &startResponse); err != nil {
		t.Fatal(err)
	}
	if startResponse["flow"] != "device" || startResponse["user_code"] != "ABCD-EFGH" {
		t.Fatalf("immediate device login response = %s", start.Body.String())
	}
	var response struct{ State string }
	if err := json.Unmarshal(start.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-saved:
		if id != "codex-device.json" {
			t.Fatalf("post-auth-persist hook received record %q", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("device auth was not persisted through the management hook")
	}
	if id := <-preSave; id != "codex-device.json" {
		t.Fatalf("post-auth hook received record %q", id)
	}
	if _, ok := store.items["codex-device.json"]; !ok {
		t.Fatal("device auth record was not saved")
	}
}

func TestWaitForCodexDeviceStartPrefersCodeWhenLoginAlreadyFinished(t *testing.T) {
	deviceCodeCh := make(chan sdkAuth.DeviceCodeInfo, 1)
	expected := sdkAuth.DeviceCodeInfo{UserCode: "ABCD-EFGH", VerificationURL: "https://auth.openai.com/codex/device", ExpiresIn: 900}
	deviceCodeCh <- expected
	loginDone := make(chan struct{})
	close(loginDone)

	info, started, timedOut := waitForCodexDeviceStart(deviceCodeCh, loginDone, nil, nil)
	if !started || timedOut || info != expected {
		t.Fatalf("waitForCodexDeviceStart() = (%#v, %t, %t), want ready code", info, started, timedOut)
	}
}

func TestWaitForCodexDeviceStartPrefersCodeWhenTimeoutFires(t *testing.T) {
	deviceCodeCh := make(chan sdkAuth.DeviceCodeInfo, 1)
	expected := sdkAuth.DeviceCodeInfo{UserCode: "ABCD-EFGH", VerificationURL: "https://auth.openai.com/codex/device", ExpiresIn: 900}
	deviceCodeCh <- expected
	timeout := make(chan time.Time, 1)
	timeout <- time.Time{}

	info, started, timedOut := waitForCodexDeviceStartAfterDrain(deviceCodeCh, nil, timeout, nil)
	if !started || timedOut || info != expected {
		t.Fatalf("waitForCodexDeviceStartAfterDrain() = (%#v, %t, %t), want ready code", info, started, timedOut)
	}
}
