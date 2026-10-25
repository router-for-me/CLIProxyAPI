package auth

import (
	"context"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestKiroNativeCredentialPersistenceAndRefreshLead(t *testing.T) {
	metadata := map[string]any{"type": "kiro", "accessToken": "test-access", "refreshToken": "test-refresh", "clientId": "test-client", "clientSecret": "test-secret", "expiresAt": "2030-01-01T00:00:00Z", "region": "us-east-1"}
	record := NewKiroAuthRecord(metadata)
	store := NewFileTokenStore()
	store.SetBaseDir(t.TempDir())
	if _, err := store.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	records, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Provider != "kiro" || records[0].Metadata["client_secret"] != "test-secret" || records[0].Metadata["access_token"] != "test-access" {
		t.Fatalf("native credential did not survive persistence")
	}
	if !coreauth.SupportsCredentialModels(records[0]) {
		t.Fatal("Kiro model editor is unavailable")
	}
	if lead := coreauth.ProviderRefreshLead("kiro", nil); lead == nil || *lead <= 0 {
		t.Fatal("Kiro proactive refresh was not registered")
	}
}
