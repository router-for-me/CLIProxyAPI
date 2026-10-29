package pluginhost

import (
	"context"
	"encoding/json"
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

type quotaDemandExecutor struct{ host *Host }

func (*quotaDemandExecutor) Identifier() string { return "plugin-provider" }
func (e *quotaDemandExecutor) Refresh(ctx context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
	auth, _, err := e.host.RefreshAuth(ctx, a)
	return auth, err
}
func (*quotaDemandExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("unexpected inference")
}
func (*quotaDemandExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("unexpected count")
}
func (*quotaDemandExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("unexpected stream")
}
func (*quotaDemandExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected HTTP")
}

func TestQuotaUsesCommittedAuthSnapshot(t *testing.T) {
	for _, operation := range []string{"fetch", "reset", "fetch-failure", "reset-failure", "id-conflict", "missing-binding"} {
		t.Run(operation, func(t *testing.T) {
			var refreshes, calls atomic.Int32
			failure := errors.New("synthetic quota refresh failure")
			quota := &testQuotaProvider{identifier: "plugin-provider"}
			check := func(raw []byte, metadata map[string]any, attributes map[string]string) {
				calls.Add(1)
				var stored map[string]any
				_ = json.Unmarshal(raw, &stored)
				if stored["access_token"] != "new" || metadata["access_token"] != "new" || metadata["refresh_token"] != "new-refresh" || attributes["api_key"] != "new" {
					t.Error("quota did not use committed tuple")
				}
			}
			quota.fetchFn = func(_ context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
				check(req.StorageJSON, req.Metadata, req.Attributes)
				return pluginapi.QuotaFetchResponse{}, nil
			}
			quota.resetFn = func(_ context.Context, req pluginapi.QuotaResetRequest) (pluginapi.QuotaResetResponse, error) {
				check(req.StorageJSON, req.Metadata, req.Attributes)
				return pluginapi.QuotaResetResponse{Success: true}, nil
			}
			host := newHostWithRecords(capabilityRecord{id: "quota-lazy", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{QuotaProvider: quota, AuthProvider: fakeAuthProvider{identifier: "plugin-provider", refreshAuth: func(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
				refreshes.Add(1)
				if operation == "fetch-failure" || operation == "reset-failure" {
					return pluginapi.AuthRefreshResponse{}, failure
				}
				return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{Metadata: map[string]any{"access_token": "new", "refresh_token": "new-refresh", "expired": time.Now().Add(time.Hour).Format(time.RFC3339)}}}, nil
			}}}}})
			dir := t.TempDir()
			store := sdkauth.NewFileTokenStore()
			store.SetBaseDir(dir)
			manager := coreauth.NewManager(store, nil, nil)
			manager.RegisterExecutor(&quotaDemandExecutor{host: host})
			auth := host.AuthDataToCoreAuth(pluginapi.AuthData{Provider: "plugin-provider", ID: "quota.json", FileName: "quota.json", RefreshPolicy: new(pluginapi.RefreshOnDemand), Metadata: map[string]any{"access_token": "old", "refresh_token": "old-refresh", "expired": "2000-01-01T00:00:00Z"}, Attributes: map[string]string{"api_key": "old"}}, filepath.Join(dir, "quota.json"), "quota.json")
			auth, _ = manager.Register(context.Background(), auth)
			if operation != "missing-binding" {
				host.SetAuthManager(manager)
			}
			id := auth.ID
			if operation == "id-conflict" {
				id = "wrong"
			}
			req := pluginapi.QuotaFetchRequest{Provider: auth.Provider, AuthID: id, AuthIndex: auth.Index, StorageJSON: []byte(`{"access_token":"stale"}`), Metadata: map[string]any{"access_token": "stale", refreshPolicyStorageKey: "on-demand"}, Attributes: map[string]string{"api_key": "stale"}}
			var handled bool
			var err error
			if operation == "reset" || operation == "reset-failure" {
				_, handled, err = host.ResetQuota(context.Background(), pluginapi.QuotaResetRequest{Provider: req.Provider, AuthID: req.AuthID, AuthIndex: req.AuthIndex, StorageJSON: req.StorageJSON, Metadata: req.Metadata, Attributes: req.Attributes})
			} else {
				_, handled, err = host.FetchQuota(context.Background(), req)
			}
			if !handled {
				t.Fatal("quota operation was not handled")
			}
			if operation == "fetch" || operation == "reset" {
				if err != nil || refreshes.Load() != 1 || calls.Load() != 1 {
					t.Fatalf("quota result=%v refreshes=%d calls=%d", err, refreshes.Load(), calls.Load())
				}
			} else {
				if err == nil || calls.Load() != 0 {
					t.Fatalf("failed quota operation dispatched: %v", err)
				}
				if operation == "fetch-failure" || operation == "reset-failure" {
					if !errors.Is(err, failure) || refreshes.Load() != 1 {
						t.Fatal("quota lost refresh error")
					}
				} else if refreshes.Load() != 0 {
					t.Fatal("unbound or conflicting auth rotated")
				}
			}
		})
	}
}
