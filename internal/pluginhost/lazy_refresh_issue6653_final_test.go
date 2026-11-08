package pluginhost

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type issue6653CountedFileStore struct {
	coreauth.Store
	saves atomic.Int32
	fail  atomic.Bool
}

func (s *issue6653CountedFileStore) Save(ctx context.Context, a *coreauth.Auth) (string, error) {
	s.saves.Add(1)
	if s.fail.Load() {
		return "", fmt.Errorf("qa disposable persistence failure")
	}
	return s.Store.Save(ctx, a)
}

func issue6653FinalParser(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	return pluginapi.AuthParseResponse{Handled: true, Auth: pluginapi.AuthData{ID: ojson.GetBytes(req.RawJSON, "id").String(), Provider: "plugin-provider", FileName: req.FileName, StorageJSON: req.RawJSON, Metadata: ojson.ParseBytes(req.RawJSON).Value().(map[string]any)}}, nil
}
func issue6653FinalPhysicalAuth(t *testing.T, h *Host, m *coreauth.Manager, store coreauth.Store, id, dir string) *coreauth.Auth {
	t.Helper()
	m.SetStore(store)
	a := issue6653ReviewAuth(h, id)
	a.FileName = id + ".json"
	a.Attributes = map[string]string{coreauth.AttributePath: filepath.Join(dir, a.FileName)}
	delete(a.Metadata, "accessToken")
	delete(a.Metadata, "refreshToken")
	a.Metadata["access_token"] = "qa-old-snake-access"
	a.Metadata["refresh_token"] = "qa-old-snake-grant"
	d := ojson.NewDoc([]byte(`{}`))
	_ = d.Set("id", id)
	_ = d.Set("plugin_owned", "preserved")
	for k, v := range a.Metadata {
		_ = d.Set(k, v)
	}
	a.Storage = &pluginTokenStorage{provider: a.Provider, rawJSON: d.Bytes(), meta: cloneAnyMap(a.Metadata)}
	registered, err := m.Register(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	return registered
}

func TestIssue6653FinalPolicyTransitionDiskAndRetry(t *testing.T) {
	for _, operator := range []bool{false, true} {
		for _, failSave := range []bool{false, true} {
			t.Run(fmt.Sprintf("operator=%v/failSave=%v", operator, failSave), func(t *testing.T) {
				started := make(chan struct{})
				release := make(chan struct{})
				var rotations atomic.Int32
				p := fakeAuthProvider{identifier: "plugin-provider", parseAuth: issue6653FinalParser, refreshAuth: func(_ context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
					rotations.Add(1)
					if req.Metadata["refresh_token"] != "qa-old-snake-grant" {
						t.Error("wrong grant at rotation")
					}
					close(started)
					<-release
					data := pluginapi.AuthData{Metadata: map[string]any{"accessToken": "qa-new-camel-access", "refreshToken": "qa-new-camel-grant", "expiresAt": time.Now().Add(time.Hour).Format(time.RFC3339Nano)}}
					if !operator {
						data.RefreshPolicy = &pluginapi.AuthRefreshPolicy{Mode: "scheduled"}
					}
					return pluginapi.AuthRefreshResponse{Auth: data}, nil
				}}
				h, m := issue6653ReviewHost(t, p)
				dir := t.TempDir()
				physical := sdkauth.NewFileTokenStore()
				physical.SetBaseDir(dir)
				store := &issue6653CountedFileStore{Store: physical}
				a := issue6653FinalPhysicalAuth(t, h, m, store, "qa-transition", dir)
				result := make(chan error, 1)
				go func() { _, err := m.EnsureAuthFresh(context.Background(), a); result <- err }()
				<-started
				if operator {
					current, _ := m.GetByID(a.ID)
					current.RefreshPolicy = &coreauth.RefreshPolicy{Mode: "scheduled"}
					if _, err := m.Update(context.Background(), current); err != nil {
						t.Fatal(err)
					}
				}
				store.fail.Store(failSave)
				close(release)
				err := <-result
				if failSave {
					if err == nil {
						t.Fatal("failed save admitted rotated credential")
					}
					store.fail.Store(false)
					if _, err := m.EnsureAuthFresh(context.Background(), a); err != nil {
						t.Fatal(err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if rotations.Load() != 1 {
					t.Fatal("save retry reused consumed old grant")
				}
				raw, err := os.ReadFile(filepath.Join(dir, a.FileName))
				if err != nil {
					t.Fatal(err)
				}
				if ojson.GetBytes(raw, "access_token").Exists() || ojson.GetBytes(raw, "refresh_token").Exists() || ojson.GetBytes(raw, "accessToken").String() != "qa-new-camel-access" || ojson.GetBytes(raw, "refreshToken").String() != "qa-new-camel-grant" || ojson.GetBytes(raw, "host_refresh_policy.mode").String() != "scheduled" || ojson.GetBytes(raw, "plugin_owned").String() != "preserved" {
					t.Fatalf("scheduled rotation saved stale pair/policy: %s", raw)
				}
				sdkauth.RegisterPluginAuthParser(h)
				defer sdkauth.RegisterPluginAuthParser(nil)
				restartStore := sdkauth.NewFileTokenStore()
				restartStore.SetBaseDir(dir)
				restart := coreauth.NewManager(restartStore, &coreauth.FillFirstSelector{}, nil)
				if err := restart.Load(context.Background()); err != nil {
					t.Fatal(err)
				}
				reloaded, ok := restart.GetByID(a.ID)
				if !ok || reloaded.RefreshPolicy == nil || reloaded.RefreshPolicy.Mode != "scheduled" || reloaded.Metadata["accessToken"] != "qa-new-camel-access" || reloaded.Metadata["refreshToken"] != "qa-new-camel-grant" {
					t.Fatal("actual-store reload restored consumed pair or wrong policy")
				}
				if _, err := restart.EnsureAuthFresh(context.Background(), reloaded); err != nil {
					t.Fatal(err)
				}
				if rotations.Load() != 1 {
					t.Fatal("restart repeated rotation")
				}
			})
		}
	}
}

func TestIssue6653FinalNestedExpiryDiskPreservesFields(t *testing.T) {
	for _, expiryAlias := range []string{"expiresAt", "expires"} {
		t.Run(expiryAlias, func(t *testing.T) {
			future := time.Now().Add(time.Hour).Truncate(time.Second)
			p := fakeAuthProvider{identifier: "plugin-provider", parseAuth: issue6653FinalParser, refreshAuth: func(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
				d := ojson.NewDoc([]byte(`{}`))
				_ = d.Set("id", "qa-nested-reload")
				_ = d.Set("siwc", map[string]any{"access_token": "qa-new-nested", "refresh_token": "qa-new-nested-grant", expiryAlias: future.Format(time.RFC3339Nano)})
				return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{StorageJSON: d.Bytes()}}, nil
			}}
			h, m := issue6653ReviewHost(t, p)
			dir := t.TempDir()
			physical := sdkauth.NewFileTokenStore()
			physical.SetBaseDir(dir)
			a := issue6653FinalPhysicalAuth(t, h, m, physical, "qa-nested-reload", dir)
			a.Metadata["use_siwc"] = true
			a.Metadata["expires_at"] = future.Format(time.RFC3339Nano)
			a.Metadata["siwc"] = map[string]any{"access_token": "qa-old-nested", "refresh_token": "qa-old-nested-grant", "expires_at": "2000-01-01T00:00:00Z", "tenant_setting": "preserved", "user_config": map[string]any{"region": "qa-loopback"}}
			if _, err := m.Update(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			fresh, err := m.EnsureAuthFresh(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			check := func(a *coreauth.Auth) {
				t.Helper()
				nested := a.Metadata["siwc"].(map[string]any)
				if nested["access_token"] != "qa-new-nested" || nested["refresh_token"] != "qa-new-nested-grant" || nested["tenant_setting"] != "preserved" || !reflect.DeepEqual(nested["user_config"], map[string]any{"region": "qa-loopback"}) {
					t.Fatalf("nested rotation lost pair/config: %v", nested)
				}
				if _, old := nested["expires_at"]; old {
					t.Fatal("obsolete nested expiry restored")
				}
				if exp, ok := a.AccessTokenExpirationTime(); !ok || !exp.Equal(future) {
					t.Fatalf("wrong nested expiry %v/%v", exp, ok)
				}
			}
			check(fresh)
			sdkauth.RegisterPluginAuthParser(h)
			defer sdkauth.RegisterPluginAuthParser(nil)
			restartStore := sdkauth.NewFileTokenStore()
			restartStore.SetBaseDir(dir)
			restart := coreauth.NewManager(restartStore, &coreauth.FillFirstSelector{}, nil)
			if err := restart.Load(context.Background()); err != nil {
				t.Fatal(err)
			}
			reloaded, ok := restart.GetByID(a.ID)
			if !ok {
				t.Fatal("nested auth missing on actual-store reload")
			}
			check(reloaded)
		})
	}
}
