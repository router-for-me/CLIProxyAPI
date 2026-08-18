package helps

import (
	"sync"
	"testing"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestEnsureClaudeCLIFingerprintIdentitySynthesizesStableSources(t *testing.T) {
	t.Parallel()

	auth := &cliproxyauth.Auth{}
	if err := EnsureClaudeCLIFingerprintIdentity(auth, "key-a", true); err != nil {
		t.Fatalf("EnsureClaudeCLIFingerprintIdentity() error = %v", err)
	}
	account := ClaudeCredentialAccountUUID(auth)
	if account == "" {
		t.Fatal("account_uuid is empty")
	}
	deviceIDs, _, errPool := claudeauth.EnsureDeviceIDPoolFor(&auth.Metadata)
	if errPool != nil {
		t.Fatalf("EnsureDeviceIDPoolFor() error = %v", errPool)
	}
	if len(deviceIDs) != 1 || deviceIDs[0] != stableClaudeCLIDeviceID("key-a") {
		t.Fatalf("device pool = %#v, want stable single device", deviceIDs)
	}

	// Second call must not rotate identity.
	if err := EnsureClaudeCLIFingerprintIdentity(auth, "key-a", true); err != nil {
		t.Fatalf("second EnsureClaudeCLIFingerprintIdentity() error = %v", err)
	}
	if got := ClaudeCredentialAccountUUID(auth); got != account {
		t.Fatalf("account_uuid changed: %q vs %q", got, account)
	}

	const sessionID = "11111111-2222-4333-8444-555555555555"
	updated, deviceID, errApply := ApplyClaudeCredentialMetadata([]byte(`{"messages":[]}`), auth, sessionID)
	if errApply != nil {
		t.Fatalf("ApplyClaudeCredentialMetadata() error = %v", errApply)
	}
	if deviceID != deviceIDs[0] {
		t.Fatalf("selected device = %q, want %q", deviceID, deviceIDs[0])
	}
	userID := gjson.GetBytes(updated, "metadata.user_id").String()
	if !IsValidUserID(userID) {
		t.Fatalf("user_id = %q, want valid", userID)
	}
	if got := gjson.Get(userID, "account_uuid").String(); got != account {
		t.Fatalf("user_id account = %q, want %q", got, account)
	}
	if got := gjson.Get(userID, "session_id").String(); got != sessionID {
		t.Fatalf("user_id session = %q, want %q", got, sessionID)
	}
}

func TestClaudeCLIAuthIdentitySeedPrefersStableAuthIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		auth *cliproxyauth.Auth
		want string
	}{
		{name: "auth ID", auth: &cliproxyauth.Auth{ID: "kimi-auth"}, want: "auth-id|kimi-auth"},
		{name: "auth index", auth: &cliproxyauth.Auth{Index: "kimi-index"}, want: "auth-index|kimi-index"},
		{name: "auth file", auth: &cliproxyauth.Auth{FileName: "kimi.json"}, want: "auth-file|kimi.json"},
		{name: "missing identity", auth: &cliproxyauth.Auth{}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ClaudeCLIAuthIdentitySeed(tt.auth); got != tt.want {
				t.Fatalf("ClaudeCLIAuthIdentitySeed() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPrepareClaudeCLIFingerprintAuthDoesNotMutateSharedMetadata(t *testing.T) {
	t.Parallel()

	shared := &cliproxyauth.Auth{
		ID: "kimi-shared",
		Metadata: map[string]any{
			"access_token": "token-1",
		},
	}
	prepared, errPrepare := PrepareClaudeCLIFingerprintAuth(shared, ClaudeCLIAuthIdentitySeed(shared), true)
	if errPrepare != nil {
		t.Fatalf("PrepareClaudeCLIFingerprintAuth() error = %v", errPrepare)
	}
	if prepared == shared {
		t.Fatal("PrepareClaudeCLIFingerprintAuth() returned the shared auth")
	}
	if ClaudeCredentialAccountUUID(shared) != "" {
		t.Fatalf("shared account_uuid = %q, want empty", ClaudeCredentialAccountUUID(shared))
	}
	if ClaudeCredentialAccountUUID(prepared) == "" {
		t.Fatal("prepared account_uuid is empty")
	}
	if _, ok := shared.Metadata[claudeauth.ClaudeDeviceIDsMetadataKey]; ok {
		t.Fatalf("shared metadata gained device pool: %#v", shared.Metadata)
	}
}

func TestPrepareClaudeCLIFingerprintAuthIsolatesUnlockedMetadataReaders(t *testing.T) {
	shared := &cliproxyauth.Auth{
		ID: "kimi-race",
		Metadata: map[string]any{
			"access_token":  "token-1",
			"refresh_token": "refresh-1",
		},
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 200 {
			prepared, errPrepare := PrepareClaudeCLIFingerprintAuth(shared, ClaudeCLIAuthIdentitySeed(shared), true)
			if errPrepare != nil {
				t.Errorf("PrepareClaudeCLIFingerprintAuth() error = %v", errPrepare)
				return
			}
			if ClaudeCredentialAccountUUID(prepared) == "" {
				t.Error("prepared account_uuid is empty")
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 200 {
			// Same unlocked read Kimi OpenAI-compat requests perform via kimiCreds.
			_ = shared.Metadata["access_token"].(string)
		}
	}()
	wg.Wait()
}

func TestEnsureClaudeCLIFingerprintIdentityNoopWithoutSynthesize(t *testing.T) {
	t.Parallel()

	auth := &cliproxyauth.Auth{}
	if err := EnsureClaudeCLIFingerprintIdentity(auth, "key-a", false); err != nil {
		t.Fatalf("EnsureClaudeCLIFingerprintIdentity() error = %v", err)
	}
	if ClaudeCredentialAccountUUID(auth) != "" {
		t.Fatal("expected no synthesized account without synthesizeMissing")
	}
}

func TestEnsureClaudeCLIFingerprintIdentityPreservesExistingOAuthSources(t *testing.T) {
	t.Parallel()

	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		"account_uuid":                        "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		claudeauth.ClaudeDeviceIDsMetadataKey: []string{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}}
	if err := EnsureClaudeCLIFingerprintIdentity(auth, "key-a", true); err != nil {
		t.Fatalf("EnsureClaudeCLIFingerprintIdentity() error = %v", err)
	}
	if got := ClaudeCredentialAccountUUID(auth); got != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("account_uuid = %q, want preserved", got)
	}
	deviceIDs, _, errPool := claudeauth.EnsureDeviceIDPoolFor(&auth.Metadata)
	if errPool != nil {
		t.Fatalf("EnsureDeviceIDPoolFor() error = %v", errPool)
	}
	if deviceIDs[0] != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("device pool mutated: %#v", deviceIDs)
	}
}
