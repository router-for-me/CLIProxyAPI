package pluginhost

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type sdkDemandQuota struct {
	calls atomic.Int32
	t     *testing.T
}

func (*sdkDemandQuota) Identifier() string { return "sdk-demand" }
func (*sdkDemandQuota) DescribeQuota(context.Context, pluginapi.QuotaDescribeRequest) (pluginapi.QuotaDescribeResponse, error) {
	return pluginapi.QuotaDescribeResponse{SupportedProviders: []string{"sdk-demand"}, SupportsReset: true}, nil
}
func (q *sdkDemandQuota) FetchQuota(_ context.Context, r pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
	q.calls.Add(1)
	if r.Metadata["access_token"] != "new" {
		q.t.Error("SDK quota received stale token")
	}
	return pluginapi.QuotaFetchResponse{}, nil
}
func (q *sdkDemandQuota) ResetQuota(_ context.Context, r pluginapi.QuotaResetRequest) (pluginapi.QuotaResetResponse, error) {
	q.calls.Add(1)
	if r.Metadata["access_token"] != "new" {
		q.t.Error("SDK reset received stale token")
	}
	return pluginapi.QuotaResetResponse{Success: true}, nil
}

type sdkDemandExecutor struct{ refreshes atomic.Int32 }

func (*sdkDemandExecutor) Identifier() string { return "sdk-demand" }
func (e *sdkDemandExecutor) Refresh(_ context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
	e.refreshes.Add(1)
	a.Metadata["access_token"] = "new"
	a.Metadata["refresh_token"] = "new-refresh"
	a.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	return a, nil
}
func (*sdkDemandExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("unexpected inference")
}
func (*sdkDemandExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("unexpected count")
}
func (*sdkDemandExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("unexpected stream")
}
func (*sdkDemandExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected HTTP")
}

func TestAuthManagerBindingRefreshesQuotaCredentials(t *testing.T) {
	host := New()
	quota := &sdkDemandQuota{t: t}
	host.inner.RegisterPluginForTest("sdk-lazy", pluginapi.Plugin{Capabilities: pluginapi.Capabilities{QuotaProvider: quota}})
	dir := t.TempDir()
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(dir)
	manager := coreauth.NewManager(store, nil, nil)
	executor := &sdkDemandExecutor{}
	manager.RegisterExecutor(executor)
	host.SetAuthManager(manager)
	auth := host.AuthDataToCoreAuth(pluginapi.AuthData{Provider: "sdk-demand", ID: "sdk.json", FileName: "sdk.json", RefreshPolicy: new(pluginapi.RefreshOnDemand), Metadata: map[string]any{"access_token": "old", "refresh_token": "old-refresh", "expired": "2000-01-01T00:00:00Z"}}, filepath.Join(dir, "sdk.json"), "sdk.json")
	auth, _ = manager.Register(context.Background(), auth)
	if _, handled, err := host.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{Provider: auth.Provider, AuthID: auth.ID, Metadata: map[string]any{"access_token": "stale"}}); err != nil || !handled {
		t.Fatalf("fetch=%v handled=%v", err, handled)
	}
	if _, handled, err := host.ResetQuota(context.Background(), pluginapi.QuotaResetRequest{Provider: auth.Provider, AuthID: auth.ID, Metadata: map[string]any{"access_token": "stale"}}); err != nil || !handled {
		t.Fatalf("reset=%v handled=%v", err, handled)
	}
	if executor.refreshes.Load() != 1 || quota.calls.Load() != 2 {
		t.Fatalf("refreshes=%d quota=%d", executor.refreshes.Load(), quota.calls.Load())
	}
}
