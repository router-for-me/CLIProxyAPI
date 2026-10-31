package pluginhost

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestAuthRefreshPolicyConversionAndRestartMarker(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy *pluginapi.RefreshPolicy
		want   pluginapi.RefreshPolicy
	}{{"omitted", nil, pluginapi.RefreshDefault}, {"default", new(pluginapi.RefreshDefault), pluginapi.RefreshDefault}, {"on-demand", new(pluginapi.RefreshOnDemand), pluginapi.RefreshOnDemand}, {"unknown", new(pluginapi.RefreshPolicy("unknown")), ""}} {
		t.Run(tc.name, func(t *testing.T) {
			host := New()
			auth := host.AuthDataToCoreAuth(pluginapi.AuthData{Provider: "plugin-provider", ID: "policy.json", RefreshPolicy: tc.policy}, "", "")
			if tc.want == "" {
				if auth != nil {
					t.Fatal("invalid direct policy accepted")
				}
				return
			}
			if auth == nil || auth.RefreshPolicy != tc.want {
				t.Fatal("wrong initial policy")
			}
			var stored map[string]any
			_ = json.Unmarshal(storageJSONFromAuth(auth), &stored)
			_, marked := stored[refreshPolicyStorageKey]
			if marked != (tc.want == pluginapi.RefreshOnDemand) {
				t.Fatal("policy persistence marker mismatch")
			}
		})
	}
	for _, value := range []string{`"unknown"`, `42`, `true`, `{}`} {
		var data pluginapi.AuthData
		if json.Unmarshal([]byte(`{"RefreshPolicy":`+value+`}`), &data) == nil {
			t.Fatalf("malformed wire policy accepted: %s", value)
		}
	}
	host := newHostWithRecords(capabilityRecord{id: "parser", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{AuthProvider: fakeAuthProvider{identifier: "plugin-provider", parseAuth: func(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
		return pluginapi.AuthParseResponse{Handled: true, Auth: pluginapi.AuthData{Provider: "plugin-provider", ID: "marker.json", StorageJSON: req.RawJSON}}, nil
	}}}}})
	auth, handled, err := host.ParseAuth(context.Background(), pluginapi.AuthParseRequest{Provider: "plugin-provider", RawJSON: []byte(`{"type":"plugin-provider","_cliproxy_refresh_policy":"on-demand"}`)})
	if err != nil || !handled || auth.RefreshPolicy != pluginapi.RefreshOnDemand {
		t.Fatalf("restart marker was not restored: %v", err)
	}
	base := host.AuthDataToCoreAuth(pluginapi.AuthData{Provider: "plugin-provider", ID: "base", RefreshPolicy: new(pluginapi.RefreshOnDemand), Metadata: map[string]any{"access_token": "old", "refresh_token": "refresh", "expired": "2000-01-01T00:00:00Z"}}, "", "")
	inherited := host.AuthDataToCoreAuth(authDataWithDefaults(pluginapi.AuthData{Label: "model-update"}, base), "", "")
	if inherited.RefreshPolicy != pluginapi.RefreshOnDemand {
		t.Fatal("partial model update waived policy")
	}
	optOut := pluginapi.AuthData{RefreshPolicy: new(pluginapi.RefreshDefault)}
	if !authDataHasValue(optOut) {
		t.Fatal("policy-only model update ignored")
	}
	converted := host.AuthDataToCoreAuth(authDataWithDefaults(optOut, base), "", "")
	var raw map[string]any
	_ = json.Unmarshal(storageJSONFromAuth(converted), &raw)
	if converted.RefreshPolicy != pluginapi.RefreshDefault || raw[refreshPolicyStorageKey] != nil {
		t.Fatal("explicit opt-out did not remove marker")
	}
}

func TestAuthRefreshAdaptersValidateCanonicalPair(t *testing.T) {
	future := "2099-01-01T00:00:00Z"
	jwt := "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":4070908800}`)) + ".sig"
	for _, adapter := range []string{"host", "executor"} {
		t.Run(adapter, func(t *testing.T) {
			for _, tc := range []struct {
				name  string
				data  pluginapi.AuthData
				bad   bool
				token string
			}{
				{"metadata", pluginapi.AuthData{Metadata: map[string]any{"access_token": "new", "expired": future}}, false, "new"},
				{"raw", pluginapi.AuthData{StorageJSON: []byte(`{"access_token":"new","refresh_token":"rotated","expired":"2099-01-01T00:00:00Z"}`)}, false, "new"},
				{"partial", pluginapi.AuthData{StorageJSON: []byte(`{"access_token":"new"}`), Metadata: map[string]any{"expired": future}}, false, "new"},
				{"same-token", pluginapi.AuthData{Metadata: map[string]any{"expired": future}}, false, "old"},
				{"jwt", pluginapi.AuthData{Metadata: map[string]any{"access_token": jwt}}, false, jwt},
				{"contradictory", pluginapi.AuthData{StorageJSON: []byte(`{"access_token":"raw"}`), Metadata: map[string]any{"access_token": "metadata", "expired": future}}, true, ""},
				{"alias-conflict", pluginapi.AuthData{Metadata: map[string]any{"access_token": "new", "accessToken": "other", "expired": future}}, true, ""},
				{"missing-expiry", pluginapi.AuthData{Metadata: map[string]any{"access_token": "new"}}, true, ""},
				{"bad-expiry", pluginapi.AuthData{Metadata: map[string]any{"access_token": "new", "expired": "invalid"}}, true, ""},
				{"identity", pluginapi.AuthData{ID: "other", Metadata: map[string]any{"access_token": "new", "expired": future}}, true, ""},
				{"provider", pluginapi.AuthData{Provider: "other", Metadata: map[string]any{"access_token": "new", "expired": future}}, true, ""},
				{"raw-identity", pluginapi.AuthData{StorageJSON: []byte(`{"id":"other","access_token":"new","expired":"2099-01-01T00:00:00Z"}`)}, true, ""},
				{"compat", pluginapi.AuthData{Metadata: map[string]any{"access_token": "new", "expired": future}, Attributes: map[string]string{"api_key": "stale"}}, true, ""},
				{"bad-json", pluginapi.AuthData{StorageJSON: []byte(`{`)}, true, ""},
			} {
				t.Run(tc.name, func(t *testing.T) {
					record := normalizeTestCapabilityRecord(capabilityRecord{id: "refresh", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{AuthProvider: fakeAuthProvider{identifier: "plugin-provider", refreshAuth: func(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
						return pluginapi.AuthRefreshResponse{Auth: tc.data}, nil
					}}}}})
					host := newHostWithRecords(record)
					base := host.AuthDataToCoreAuth(pluginapi.AuthData{Provider: "plugin-provider", ID: "base", RefreshPolicy: new(pluginapi.RefreshOnDemand), StorageJSON: []byte(`{"accessToken":"old","refreshToken":"refresh","expiresAt":"2000-01-01T00:00:00Z"}`), Metadata: map[string]any{"access_token": "old", "refresh_token": "refresh", "expired": "2000-01-01T00:00:00Z"}, Attributes: map[string]string{"api_key": "old"}}, "", "")
					var next *coreauth.Auth
					var err error
					if adapter == "host" {
						next, _, err = host.RefreshAuth(context.Background(), base)
					} else {
						exec := newExecutorAdapterForRecordForTest(host, record, &fakeExecutor{identifier: "plugin-provider"}, nil, nil)
						next, err = exec.Refresh(context.Background(), base)
					}
					if tc.bad {
						if err == nil {
							t.Fatal("invalid refresh accepted")
						}
						return
					}
					if err != nil || next == nil {
						t.Fatalf("coherent partial result rejected: %v", err)
					}
					var raw map[string]any
					_ = json.Unmarshal(storageJSONFromAuth(next), &raw)
					if next.Metadata["access_token"] != tc.token || raw["access_token"] != tc.token || next.Attributes["api_key"] != tc.token || next.RefreshPolicy != pluginapi.RefreshOnDemand {
						t.Fatal("canonical bearer views disagree")
					}
					if raw["accessToken"] != nil || raw["refreshToken"] != nil || raw["expiresAt"] != nil {
						t.Fatal("stale token aliases survived rotation")
					}
					if next.Metadata["refresh_token"] == nil || raw["refresh_token"] != next.Metadata["refresh_token"] {
						t.Fatal("omitted refresh credential not inherited")
					}
				})
			}
		})
	}
}

func TestAuthRefreshStorageSnapshotsAreIsolated(t *testing.T) {
	host := New()
	auth := host.AuthDataToCoreAuth(pluginapi.AuthData{Provider: "plugin-provider", ID: "base", Metadata: map[string]any{"access_token": "old", "refresh_token": "refresh", "expired": "2000-01-01T00:00:00Z"}}, "", "")
	clone := auth.Clone()
	clone.Metadata["access_token"] = "new"
	clone.Storage.(*pluginTokenStorage).SetMetadata(clone.Metadata)
	var raw map[string]any
	_ = json.Unmarshal(storageJSONFromAuth(auth), &raw)
	if raw["access_token"] != "old" {
		t.Fatal("clone mutated published storage")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				snapshot := auth.Clone()
				snapshot.Storage.(*pluginTokenStorage).SetMetadata(snapshot.Metadata)
				_ = storageJSONFromAuth(auth)
			}
		}()
	}
	wg.Wait()
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := clone.Storage.SaveTokenToFile(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestAuthRefreshPolicyEditCannotWaiveStrictSave(t *testing.T) {
	host := New()
	base := host.AuthDataToCoreAuth(pluginapi.AuthData{Provider: "plugin-provider", ID: "base", RefreshPolicy: new(pluginapi.RefreshOnDemand), Metadata: map[string]any{"access_token": "old", "refresh_token": "refresh", "expired": "2000-01-01T00:00:00Z"}}, "", "")
	updated, err := host.refreshedAuth(base, pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{RefreshPolicy: new(pluginapi.RefreshDefault), Metadata: map[string]any{"access_token": "new", "expired": time.Now().Add(time.Hour).Format(time.RFC3339)}}})
	if err != nil {
		t.Fatal(err)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	base, _ = manager.Register(context.Background(), base)
	_, err = manager.UpdateRefreshedAuth(coreauth.WithSkipPersist(context.Background()), base, updated)
	if err == nil {
		t.Fatal("partial policy opt-out waived strict rotation save")
	}
	current, _ := manager.GetByID(base.ID)
	if current.Metadata["access_token"] != "old" {
		t.Fatal("unsaved rotated credential published")
	}

}
