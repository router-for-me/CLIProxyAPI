package pluginhost

import (
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestIssue6653BaselinePluginAuthDataExpiryGate(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	host := New()
	a := host.AuthDataToCoreAuth(pluginapi.AuthData{
		ID: "qa-native-expired", Provider: "qa-native-6653",
		StorageJSON: []byte(`{"access_token":"qa-access","refresh_token":"qa-refresh","expires_at":"2025-12-31T23:00:00Z"}`),
		Metadata:    map[string]any{"access_token": "qa-access", "refresh_token": "qa-refresh", "expires_at": now.Add(-time.Hour).Format(time.RFC3339)},
	}, "", "")
	if a == nil {
		t.Fatal("plugin auth mapping returned nil")
	}
	if coreauth.IsAuthAvailableForModel(a, "", now) {
		t.Fatal("expired plugin auth unexpectedly selectable")
	}
	if a.Metadata["refresh_token"] != "qa-refresh" {
		t.Fatal("refresh token lost during mapping")
	}
	t.Log("truthful native-contract AuthData retains refresh token but expired access is ineligible")
}
