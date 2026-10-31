package pluginhost

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestAuthRefreshInitialRawTokenProjection(t *testing.T) {
	data := pluginapi.AuthData{Provider: "plugin-provider", ID: "raw", RefreshPolicy: new(pluginapi.RefreshOnDemand), StorageJSON: []byte(`{"access_token":"raw-access","refresh_token":"raw-refresh","expires_at":"2099-01-01T00:00:00Z"}`)}
	auth := New().AuthDataToCoreAuth(data, "", "")
	if auth == nil || auth.Metadata["access_token"] != "raw-access" || auth.Metadata["refresh_token"] != "raw-refresh" {
		t.Fatal("raw credential was not projected into host selection metadata")
	}
	if _, known := auth.AccessTokenExpirationTime(); !known {
		t.Fatal("raw expiry was hidden from selection")
	}
	data.Metadata = map[string]any{"access_token": "contradiction"}
	if New().AuthDataToCoreAuth(data, "", "") != nil {
		t.Fatal("initial contradictory managed tuple accepted")
	}
}

func TestAuthRefreshConcurrentPolicyEditPersistsOptOut(t *testing.T) {
	host := New()
	dir := t.TempDir()
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(dir)
	manager := coreauth.NewManager(store, nil, nil)
	base := host.AuthDataToCoreAuth(pluginapi.AuthData{Provider: "plugin-provider", ID: "policy.json", FileName: "policy.json", RefreshPolicy: new(pluginapi.RefreshOnDemand), Metadata: map[string]any{"access_token": "old", "refresh_token": "refresh", "expired": "2000-01-01T00:00:00Z"}}, filepath.Join(dir, "policy.json"), "policy.json")
	base, _ = manager.Register(context.Background(), base)
	updated, err := host.refreshedAuth(base, pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{Metadata: map[string]any{"access_token": "new", "expired": "2099-01-01T00:00:00Z"}}})
	if err != nil {
		t.Fatal(err)
	}
	current := base.Clone()
	current.RefreshPolicy = pluginapi.RefreshDefault
	delete(current.Metadata, refreshPolicyStorageKey)
	if _, err := manager.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	merged, err := manager.UpdateRefreshedAuth(context.Background(), base, updated)
	if err != nil {
		t.Fatal(err)
	}
	if merged.RefreshPolicy != pluginapi.RefreshDefault {
		t.Fatal("concurrent opt-out lost")
	}
	var stored map[string]any
	_ = json.Unmarshal(storageJSONFromAuth(merged), &stored)
	if stored[refreshPolicyStorageKey] != nil {
		t.Fatal("opt-out left persisted policy marker")
	}
}
