package pluginhost

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/MoeForge/moego/ojson"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func issue6653ReviewHost(t *testing.T, provider fakeAuthProvider) (*Host, *coreauth.Manager) {
	t.Helper()
	p := pluginapi.Plugin{Capabilities: pluginapi.Capabilities{AuthProvider: provider, Executor: &fakeExecutor{identifier: "plugin-provider"}}}
	r := capabilityRecord{id: "qa-review", plugin: p}
	h := newHostWithRecords(r)
	m := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
	h.SetAuthManager(m)
	m.RegisterExecutor(newExecutorAdapterForRecordForTest(h, r, p.Capabilities.Executor, nil, nil))
	return h, m
}
func issue6653ReviewAuth(h *Host, id string) *coreauth.Auth {
	return h.AuthDataToCoreAuth(pluginapi.AuthData{ID: id, Provider: "plugin-provider", RefreshPolicy: &pluginapi.AuthRefreshPolicy{Mode: "lazy"}, Metadata: map[string]any{"accessToken": "qa-old-access", "refreshToken": "qa-old-grant", "expires_at": time.Now().Add(-time.Hour).Format(time.RFC3339Nano), "qa_setting": "preserved"}}, "", "")
}

func TestIssue6653ReviewCredentialProjectionAndDisk(t *testing.T) {
	expiry := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	shapes := []string{"expired", "expire", "expires_at", "expiresAt", "expiry", "expires", "token", "Token", "expires_in/timestamp", "expiresIn/issued_at", "expiresIn/issuedAt", "expires_in/issued_at", "expires_in/issuedAt", "expiresIn/timestamp"}
	for _, shape := range shapes {
		for _, metadataOnly := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/metadata=%v", shape, metadataOnly), func(t *testing.T) {
				d := ojson.NewDoc([]byte(`{}`))
				_ = d.Set("accessToken", "qa-new-access")
				_ = d.Set("refreshToken", "qa-new-grant")
				switch shape {
				case "token", "Token":
					_ = d.Set(shape, map[string]any{"expires_at": expiry.Format(time.RFC3339Nano)})
				case "expires_in/timestamp", "expiresIn/timestamp":
					relativeKey := "expires_in"
					if shape == "expiresIn/timestamp" {
						relativeKey = "expiresIn"
					}
					_ = d.Set(relativeKey, 7200)
					_ = d.Set("timestamp", expiry.Add(-2*time.Hour).Format(time.RFC3339Nano))
				case "expiresIn/issued_at", "expiresIn/issuedAt", "expires_in/issued_at", "expires_in/issuedAt":
					relativeKey := "expiresIn"
					if strings.HasPrefix(shape, "expires_in/") {
						relativeKey = "expires_in"
					}
					_ = d.Set(relativeKey, 7200)
					key := "issued_at"
					if strings.HasSuffix(shape, "/issuedAt") {
						key = "issuedAt"
					}
					_ = d.Set(key, expiry.Add(-2*time.Hour).Format(time.RFC3339Nano))
				default:
					_ = d.Set(shape, expiry.Format(time.RFC3339Nano))
				}
				freshMap := ojson.ParseBytes(d.Bytes()).Map()
				explicit := make(map[string]any)
				for k, v := range freshMap {
					explicit[k] = v.Value()
				}
				provider := fakeAuthProvider{identifier: "plugin-provider", refreshAuth: func(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
					data := pluginapi.AuthData{StorageJSON: d.Bytes()}
					if metadataOnly {
						data.StorageJSON = nil
						data.Metadata = explicit
					}
					return pluginapi.AuthRefreshResponse{Auth: data}, nil
				}}
				h, m := issue6653ReviewHost(t, provider)
				a := issue6653ReviewAuth(h, "qa-projection")
				// Start with the opposite token aliases in inherited metadata and raw storage.
				a.Metadata["access_token"] = "qa-old-access"
				a.Metadata["refresh_token"] = "qa-old-grant"
				a.Storage = &pluginTokenStorage{provider: a.Provider, rawJSON: []byte(`{"access_token":"qa-old-access","refresh_token":"qa-old-grant","expires_at":"2000-01-01T00:00:00Z","plugin_owned":"preserved"}`), meta: cloneAnyMap(a.Metadata)}
				a, err := m.Register(context.Background(), a)
				if err != nil {
					t.Fatal(err)
				}
				fresh, err := m.EnsureAuthFresh(context.Background(), a)
				if err != nil {
					t.Fatal(err)
				}
				check := func(a *coreauth.Auth) {
					t.Helper()
					if a.Metadata["accessToken"] != "qa-new-access" || a.Metadata["refreshToken"] != "qa-new-grant" {
						t.Fatalf("rotated camelCase pair lost: %v", a.Metadata)
					}
					if got, ok := a.AccessTokenExpirationTime(); !ok || !got.Equal(expiry) {
						t.Fatalf("expiry alias %s got=%v known=%v", shape, got, ok)
					}
				}
				check(fresh)
				file := filepath.Join(t.TempDir(), "qa.json")
				if err := fresh.Storage.SaveTokenToFile(file); err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if ojson.GetBytes(raw, "access_token").Exists() || ojson.GetBytes(raw, "refresh_token").Exists() {
					t.Fatalf("old snake aliases survived serialization: %s", raw)
				}
				if ojson.GetBytes(raw, "accessToken").String() != "qa-new-access" || ojson.GetBytes(raw, "refreshToken").String() != "qa-new-grant" {
					t.Fatalf("saved rotated pair lost: %s", raw)
				}
				reloaded := New().AuthDataToCoreAuth(pluginapi.AuthData{Provider: a.Provider, StorageJSON: raw, Metadata: ojson.ParseBytes(raw).Value().(map[string]any)}, "", "")
				check(reloaded)
			})
		}
	}
}

func TestIssue6653ReviewNativeTerminalCodes(t *testing.T) {
	for _, code := range []string{"invalid_grant", "token_revoked"} {
		for _, status := range []int{0, 400} {
			t.Run(fmt.Sprintf("%s/status=%d", code, status), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					calls := 0
					p := fakeAuthProvider{identifier: "plugin-provider", refreshAuth: func(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
						calls++
						return pluginapi.AuthRefreshResponse{}, rpcError{Code: code, message: "renewal rejected", statusCode: status}
					}}
					h, m := issue6653ReviewHost(t, p)
					a, err := m.Register(context.Background(), issue6653ReviewAuth(h, "qa-terminal"))
					if err != nil {
						t.Fatal(err)
					}
					if _, err := m.EnsureAuthFresh(context.Background(), a); err == nil {
						t.Fatal("terminal error accepted")
					}
					current, _ := m.GetByID(a.ID)
					if !current.RequiresSignIn || current.RejectedRefreshToken == "" {
						t.Fatal("structured terminal error lacks sign-in/rejected grant state")
					}
					time.Sleep(time.Hour)
					if _, err := m.EnsureAuthFresh(context.Background(), a); err == nil {
						t.Fatal("terminal error recovered without replacement")
					}
					if calls != 1 {
						t.Fatalf("rejected grant reused calls=%d", calls)
					}
				})
			})
		}
	}
}

func TestIssue6653ReviewActualMultiAuthStoreRejectsRotation(t *testing.T) {
	calls := 0
	p := fakeAuthProvider{identifier: "plugin-provider", refreshAuth: func(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
		calls++
		t.Error("virtual source rotated")
		return pluginapi.AuthRefreshResponse{}, nil
	}}
	h, m := issue6653ReviewHost(t, p)
	p.parseAuth = func(context.Context, pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
		return pluginapi.AuthParseResponse{Handled: true, Auths: []pluginapi.AuthData{{ID: "qa-multi-a", Provider: "plugin-provider", RefreshPolicy: &pluginapi.AuthRefreshPolicy{Mode: "lazy"}, Metadata: issue6653ReviewAuth(h, "unused").Metadata}, {ID: "qa-multi-b", Provider: "plugin-provider", RefreshPolicy: &pluginapi.AuthRefreshPolicy{Mode: "lazy"}, Metadata: issue6653ReviewAuth(h, "unused").Metadata}}}, nil
	}
	h, m = issue6653ReviewHost(t, p)
	sdkauth.RegisterPluginAuthParser(h)
	defer sdkauth.RegisterPluginAuthParser(nil)
	dir := t.TempDir()
	file := filepath.Join(dir, "multi.json")
	raw := []byte(`{"type":"plugin-provider","accessToken":"qa-old-access","refreshToken":"qa-old-grant"}`)
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(dir)
	m.SetStore(store)
	if err := m.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"qa-multi-a", "qa-multi-b"} {
		a, ok := m.GetByID(id)
		if !ok || !coreauth.IsPluginVirtualAuth(a) {
			t.Fatalf("real multi-auth file did not expand into virtual %s", id)
		}
		if _, err := m.EnsureAuthFresh(context.Background(), a); err == nil {
			t.Fatal("virtual auth accepted")
		}
	}
	saved, err := os.ReadFile(file)
	if err != nil || string(saved) != string(raw) || calls != 0 {
		t.Fatal("multi-auth source modified/rotated")
	}
	restart := coreauth.NewManager(store, &coreauth.FillFirstSelector{}, nil)
	if err := restart.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"qa-multi-a", "qa-multi-b"} {
		a, ok := restart.GetByID(id)
		if !ok || a.Metadata["refreshToken"] != "qa-old-grant" {
			t.Fatal("restart lost untouched multi-auth grant")
		}
	}
}

func TestIssue6653ReviewQuotaUsesLatestProxy(t *testing.T) {
	var oldCalls, newCalls atomic.Int32
	oldProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { oldCalls.Add(1); t.Error("old caller proxy used") }))
	defer oldProxy.Close()
	newProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		newCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer qa-new-access" {
			t.Error("proxy got stale quota token")
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer newProxy.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	ap := fakeAuthProvider{identifier: "plugin-provider", refreshAuth: func(_ context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
		close(started)
		<-release
		data := pluginapi.AuthData{Metadata: map[string]any{"accessToken": "qa-new-access", "refreshToken": "qa-new-grant", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339Nano)}}
		return pluginapi.AuthRefreshResponse{Auth: data}, nil
	}}
	qp := &testQuotaProvider{identifier: "plugin-provider", fetchFn: func(ctx context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
		_, err := req.HTTPClient.Do(ctx, pluginapi.HTTPRequest{Method: http.MethodGet, URL: "http://127.0.0.1:1/quota", Headers: http.Header{"Authorization": []string{"Bearer " + req.Metadata["accessToken"].(string)}}})
		return pluginapi.QuotaFetchResponse{}, err
	}}
	p := pluginapi.Plugin{Capabilities: pluginapi.Capabilities{AuthProvider: ap, Executor: &fakeExecutor{identifier: "plugin-provider"}, QuotaProvider: qp}}
	record := capabilityRecord{id: "qa-review", plugin: p}
	h := newHostWithRecords(record)
	m := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
	h.SetAuthManager(m)
	m.RegisterExecutor(newExecutorAdapterForRecordForTest(h, record, p.Capabilities.Executor, nil, nil))
	a := issue6653ReviewAuth(h, "qa-proxy")
	a.ProxyURL = oldProxy.URL
	a, err := m.Register(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := h.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{Provider: a.Provider, AuthID: a.ID, Metadata: a.Metadata})
		done <- err
	}()
	<-started
	current, _ := m.GetByID(a.ID)
	current.ProxyURL = newProxy.URL
	current.Metadata["proxy_url"] = newProxy.URL
	if _, err := m.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if oldCalls.Load() != 0 || newCalls.Load() != 1 {
		t.Fatalf("latest proxy transport old=%d new=%d", oldCalls.Load(), newCalls.Load())
	}
}

func TestIssue6653ReviewAliasDirectionAndNestedStorage(t *testing.T) {
	for _, shape := range []string{"camel-to-snake", "nested-siwc"} {
		t.Run(shape, func(t *testing.T) {
			future := time.Now().Add(time.Hour).Truncate(time.Second).Format(time.RFC3339Nano)
			p := fakeAuthProvider{identifier: "plugin-provider", refreshAuth: func(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
				if shape == "camel-to-snake" {
					return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{Metadata: map[string]any{"access_token": "qa-new-access", "refresh_token": "qa-new-grant", "expiresAt": future}}}, nil
				}
				d := ojson.NewDoc([]byte(`{}`))
				_ = d.Set("siwc", map[string]any{"access_token": "qa-new-nested", "refresh_token": "qa-new-nested-grant", "expires": future})
				return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{StorageJSON: d.Bytes()}}, nil
			}}
			h, m := issue6653ReviewHost(t, p)
			a := issue6653ReviewAuth(h, "qa-alias")
			if shape == "nested-siwc" {
				a.Metadata["use_siwc"] = true
				a.Metadata["expires_at"] = future
				a.Metadata["siwc"] = map[string]any{"access_token": "qa-old-nested", "refresh_token": "qa-old-nested-grant", "expires_at": "2000-01-01T00:00:00Z"}
			}
			d := ojson.NewDoc([]byte(`{}`))
			for k, v := range a.Metadata {
				_ = d.Set(k, v)
			}
			a.Storage = &pluginTokenStorage{provider: a.Provider, rawJSON: d.Bytes(), meta: cloneAnyMap(a.Metadata)}
			a, err := m.Register(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := m.EnsureAuthFresh(context.Background(), a)
			if err != nil {
				latest, _ := m.GetByID(a.ID)
				t.Fatalf("rotated nested/alias refresh failed: %v; current siwc=%v", err, latest.Metadata["siwc"])
			}
			raw := storageJSONFromAuth(fresh)
			if shape == "camel-to-snake" {
				if ojson.GetBytes(raw, "accessToken").Exists() || ojson.GetBytes(raw, "refreshToken").Exists() || ojson.GetBytes(raw, "access_token").String() != "qa-new-access" || ojson.GetBytes(raw, "refresh_token").String() != "qa-new-grant" {
					t.Fatalf("metadata-only snake rotation preserved old camel aliases: %s", raw)
				}
			} else {
				if ojson.GetBytes(raw, "siwc.access_token").String() != "qa-new-nested" || ojson.GetBytes(raw, "siwc.refresh_token").String() != "qa-new-nested-grant" || ojson.GetBytes(raw, "siwc.expires_at").Exists() {
					t.Fatalf("nested storage rotation lost pair or retained old expiry: %s", raw)
				}
			}
			if exp, ok := fresh.AccessTokenExpirationTime(); !ok || !exp.After(time.Now()) {
				t.Fatal("rotated alias/nested token not fresh")
			}
		})
	}
}
