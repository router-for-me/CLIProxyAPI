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

type authRefreshCompatCounter struct{ calls atomic.Int32 }

func (*authRefreshCompatCounter) Identifier() string { return "auth-refresh-valid-compat" }
func (*authRefreshCompatCounter) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, errors.New("fresh control must not rotate")
}
func (e *authRefreshCompatCounter) Execute(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls.Add(1)
	if auth.Attributes["api_key"] != auth.Metadata["access_token"] {
		return coreexecutor.Response{}, errors.New("compat bearer contradicts access token")
	}
	return coreexecutor.Response{}, nil
}
func (*authRefreshCompatCounter) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}
func (*authRefreshCompatCounter) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, nil
}
func (*authRefreshCompatCounter) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestAuthRefreshInitialCompatBearer(t *testing.T) {
	for _, mode := range []string{"contradictory-attribute", "contradictory-metadata", "coherent", "raw-only", "metadata-only", "opt-out"} {
		t.Run(mode, func(t *testing.T) {
			host := New()
			dir := t.TempDir()
			executor := &authRefreshCompatCounter{}
			metadata := map[string]any{"access_token": "truthful-access", "refresh_token": "truthful-refresh", "expired": time.Now().Add(time.Hour).Format(time.RFC3339Nano)}
			raw, err := json.Marshal(metadata)
			if err != nil {
				t.Fatal(err)
			}
			data := pluginapi.AuthData{Provider: executor.Identifier(), ID: "initial.json", FileName: "initial.json", RefreshPolicy: new(pluginapi.RefreshOnDemand), StorageJSON: raw, Metadata: metadata, Attributes: map[string]string{"api_key": "truthful-access"}}
			switch mode {
			case "contradictory-attribute", "opt-out":
				data.Attributes["api_key"] = "wrong-bearer"
			case "contradictory-metadata":
				data.Metadata["api_key"] = "wrong-bearer"
			case "raw-only":
				data.Metadata = nil
			case "metadata-only":
				data.StorageJSON = nil
			}
			if mode == "opt-out" {
				data.RefreshPolicy = nil
			}
			auth := host.AuthDataToCoreAuth(data, filepath.Join(dir, data.FileName), data.FileName)
			if mode == "contradictory-attribute" || mode == "contradictory-metadata" {
				if auth != nil {
					t.Fatal("contradictory initial compat bearer accepted for inference")
				}
				return
			}
			if auth == nil {
				t.Fatal("coherent initial credential was rejected")
			}
			if mode == "opt-out" {
				if auth.Attributes["api_key"] != "wrong-bearer" {
					t.Fatal("legacy opt-out attributes changed")
				}
				return
			}
			store := sdkauth.NewFileTokenStore()
			store.SetBaseDir(dir)
			manager := coreauth.NewManager(store, nil, nil)
			manager.RegisterExecutor(executor)
			selected, err := manager.Register(context.Background(), auth)
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := manager.EnsureRequestAuth(context.Background(), selected, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := executor.Execute(context.Background(), fresh, coreexecutor.Request{}, coreexecutor.Options{}); err != nil || executor.calls.Load() != 1 {
				t.Fatalf("fresh coherent credential did not execute: err=%v calls=%d", err, executor.calls.Load())
			}
		})
	}
}

func TestQuotaAuthLookupCompatibility(t *testing.T) {
	for _, operation := range []string{"fetch", "reset"} {
		t.Run(operation, func(t *testing.T) {
			for _, mode := range []string{"unmanaged", "explicit-managed", "registered-managed", "identity-conflict"} {
				t.Run(mode, func(t *testing.T) {
					provider := &testQuotaProvider{identifier: "plugin-provider"}
					var calls atomic.Int32
					check := func(index string, metadata map[string]any) {
						calls.Add(1)
						if index != "unmanaged-old-index" || metadata["access_token"] != "caller-owned-valid" {
							t.Error("unmanaged caller-owned quota context changed")
						}
					}
					provider.fetchFn = func(_ context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
						check(req.AuthIndex, req.Metadata)
						return pluginapi.QuotaFetchResponse{}, nil
					}
					provider.resetFn = func(_ context.Context, req pluginapi.QuotaResetRequest) (pluginapi.QuotaResetResponse, error) {
						check(req.AuthIndex, req.Metadata)
						return pluginapi.QuotaResetResponse{Success: true}, nil
					}
					host := newHostWithRecords(capabilityRecord{id: "quota-legacy", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{QuotaProvider: provider}}})
					manager := coreauth.NewManager(nil, nil, nil)
					host.SetAuthManager(manager)
					metadata := map[string]any{"access_token": "caller-owned-valid"}
					id, index := "", "unmanaged-old-index"
					if mode == "explicit-managed" {
						metadata[refreshPolicyStorageKey] = string(pluginapi.RefreshOnDemand)
					}
					if mode == "registered-managed" || mode == "identity-conflict" {
						auth, err := manager.Register(context.Background(), &coreauth.Auth{ID: "registered", Provider: "plugin-provider", RefreshPolicy: pluginapi.RefreshOnDemand})
						if err != nil {
							t.Fatal(err)
						}
						id = auth.ID
						if mode == "identity-conflict" {
							id, index = "other", auth.Index
						}
					}
					var handled bool
					var err error
					if operation == "fetch" {
						_, handled, err = host.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{Provider: "plugin-provider", AuthID: id, AuthIndex: index, Metadata: metadata})
					} else {
						_, handled, err = host.ResetQuota(context.Background(), pluginapi.QuotaResetRequest{Provider: "plugin-provider", AuthID: id, AuthIndex: index, Metadata: metadata})
					}
					if mode == "unmanaged" {
						if !handled || err != nil || calls.Load() != 1 {
							t.Fatalf("legacy quota dispatch changed: handled=%v err=%v calls=%d", handled, err, calls.Load())
						}
					} else if !handled || err == nil || calls.Load() != 0 {
						t.Fatalf("unbound managed quota dispatched: handled=%v err=%v calls=%d", handled, err, calls.Load())
					}
				})
			}
		})
	}
}

func TestAuthRefreshConversionFailureQuarantinesRotation(t *testing.T) {
	for _, adapter := range []string{"host", "executor"} {
		t.Run(adapter, func(t *testing.T) {
			var refreshes atomic.Int32
			record := normalizeTestCapabilityRecord(capabilityRecord{id: "invalid-rotation", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{AuthProvider: fakeAuthProvider{identifier: "plugin-provider", refreshAuth: func(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
				refreshes.Add(1)
				return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{StorageJSON: []byte(`{"access_token":"raw-rotated","refresh_token":"rotated-refresh","expired":"2099-01-01T00:00:00Z"}`), Metadata: map[string]any{"access_token": "contradictory-rotated"}}}, nil
			}}}}})
			host := newHostWithRecords(record)
			dir := t.TempDir()
			store := sdkauth.NewFileTokenStore()
			store.SetBaseDir(dir)
			manager := coreauth.NewManager(store, nil, nil)
			if adapter == "host" {
				manager.RegisterExecutor(&quotaDemandExecutor{host: host})
			} else {
				manager.RegisterExecutor(newExecutorAdapterForRecordForTest(host, record, &fakeExecutor{identifier: "plugin-provider"}, nil, nil))
			}
			auth := host.AuthDataToCoreAuth(pluginapi.AuthData{Provider: "plugin-provider", ID: "rotation.json", FileName: "rotation.json", RefreshPolicy: new(pluginapi.RefreshOnDemand), Metadata: map[string]any{"access_token": "old", "refresh_token": "old-refresh", "expired": "2000-01-01T00:00:00Z"}}, filepath.Join(dir, "rotation.json"), "rotation.json")
			selected, err := manager.Register(context.Background(), auth)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.EnsureRequestAuth(context.Background(), selected, ""); err == nil {
				t.Fatal("contradictory rotation accepted")
			}
			if _, err := manager.ForceRefreshAuth(context.Background(), selected.ID); err == nil || refreshes.Load() != 1 {
				t.Fatalf("conversion error replayed consumed grant: err=%v refreshes=%d", err, refreshes.Load())
			}
		})
	}
}
