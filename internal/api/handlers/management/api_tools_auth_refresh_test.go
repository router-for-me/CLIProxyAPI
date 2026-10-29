package management

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type billingDemandExecutor struct {
	refreshes atomic.Int32
	failure   error
}

func (*billingDemandExecutor) Identifier() string { return "billing-demand" }
func (e *billingDemandExecutor) Refresh(_ context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
	e.refreshes.Add(1)
	if e.failure != nil {
		return nil, e.failure
	}
	a.Metadata["access_token"] = "synthetic-billing-new"
	a.Metadata["refresh_token"] = "synthetic-billing-refresh-new"
	a.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	return a, nil
}
func (*billingDemandExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("unexpected inference")
}
func (*billingDemandExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("unexpected count")
}
func (*billingDemandExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("unexpected stream")
}
func (*billingDemandExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected HTTP")
}

func TestAPICallPlaceholderUsesManagedToken(t *testing.T) {
	for _, mode := range []string{"success", "failure"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			store := sdkauth.NewFileTokenStore()
			store.SetBaseDir(dir)
			manager := coreauth.NewManager(store, nil, nil)
			executor := &billingDemandExecutor{}
			if mode == "failure" {
				executor.failure = errors.New("synthetic actual billing refresh failure")
			}
			manager.RegisterExecutor(executor)
			host := pluginhost.New()
			auth := host.AuthDataToCoreAuth(pluginapi.AuthData{Provider: executor.Identifier(), ID: "billing.json", FileName: "billing.json", RefreshPolicy: new(pluginapi.RefreshOnDemand), Metadata: map[string]any{"access_token": "synthetic-billing-old", "refresh_token": "synthetic-billing-refresh-old", "expired": "2000-01-01T00:00:00Z"}}, filepath.Join(dir, "billing.json"), "billing.json")
			auth, _ = manager.Register(context.Background(), auth)
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer synthetic-billing-new" {
					t.Error("billing used stale bearer")
				}
				body, _ := io.ReadAll(r.Body)
				if string(body) != `{"token":"synthetic-billing-new"}` {
					t.Errorf("billing body=%s", body)
				}
				_, _ = io.WriteString(w, `{"balance":1}`)
			}))
			defer upstream.Close()
			handler := NewHandler(&config.Config{}, "", manager)
			router := gin.New()
			router.POST("/billing", handler.APICall)
			payload, _ := json.Marshal(map[string]any{"auth_index": auth.Index, "method": "POST", "url": upstream.URL, "header": map[string]string{"Authorization": "Bearer $TOKEN$"}, "data": `{"token":"$TOKEN$"}`})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest("POST", "/billing", strings.NewReader(string(payload))))
			if mode == "success" {
				if response.Code != 200 || calls.Load() != 1 {
					t.Fatalf("billing=%d calls=%d body=%s", response.Code, calls.Load(), response.Body.String())
				}
			} else {
				if calls.Load() != 0 || !strings.Contains(response.Body.String(), executor.failure.Error()) {
					t.Fatalf("lost error or dispatched failure: %s", response.Body.String())
				}
			}
			if executor.refreshes.Load() != 1 {
				t.Fatalf("refreshes=%d", executor.refreshes.Load())
			}
		})
	}
}
