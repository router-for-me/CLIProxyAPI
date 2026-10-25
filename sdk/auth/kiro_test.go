package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestKiroRawImportIdentitySurvivesRepeatedReads(t *testing.T) {
	for _, payload := range []string{
		`{"email":"user@example.com","provider":"Github","refreshToken":"fixture-refresh"}`,
		`{"provider":"Github","refreshToken":"fixture-refresh"}`,
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "kiro.json")
		if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
		store := NewFileTokenStore()
		store.SetBaseDir(dir)
		var identity string
		for range 2 {
			records, err := store.List(context.Background())
			if err != nil || len(records) != 1 {
				t.Fatalf("read raw import: count=%d error=%v", len(records), err)
			}
			got, _ := records[0].Metadata["machine_id"].(string)
			if got == "" || identity != "" && got != identity {
				t.Fatalf("identity changed after reloading: %q != %q", got, identity)
			}
			identity = got
		}
		if raw, err := os.ReadFile(path); err != nil || string(raw) != payload {
			t.Fatal("listing credentials modified the source file")
		}
	}
}

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
