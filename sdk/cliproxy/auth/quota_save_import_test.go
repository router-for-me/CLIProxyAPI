package auth_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestQuotaRecoverySavedCredentialIdentity(t *testing.T) {
	for _, tc := range []struct {
		name          string
		siwc, recover bool
		change        func(map[string]any)
	}{
		{name: "same_outer", recover: true, change: func(map[string]any) {}},
		{name: "same_siwc", siwc: true, recover: true, change: func(map[string]any) {}},
		{name: "account_same_bearer", change: func(m map[string]any) { m["account_id"] = "account-b" }},
		{name: "outer_access_rotation", change: func(m map[string]any) { m["access_token"] = "other-access" }},
		{name: "outer_refresh_rotation", change: func(m map[string]any) { m["refresh_token"] = "other-refresh" }},
		{name: "siwc_access_rotation", siwc: true, change: func(m map[string]any) { m["siwc"].(map[string]any)["access_token"] = "other-siwc-access" }},
		{name: "siwc_account_replacement", siwc: true, change: func(m map[string]any) { m["siwc"].(map[string]any)["id_token"] = saveImportIDToken("account-b") }},
		{name: "enable_siwc", change: func(m map[string]any) { m["use_siwc"] = true; m["siwc"] = saveImportSIWC("account-a") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prior := coreauth.QuotaCooldownDisabledForAuth(nil)
			coreauth.SetQuotaCooldownDisabled(false)
			t.Cleanup(func() { coreauth.SetQuotaCooldownDisabled(prior) })
			ctx := context.Background()
			manager := coreauth.NewManager(nil, nil, nil)
			cooldown := coreauth.NewFileCooldownStateStore(t.TempDir())
			manager.SetCooldownStateStore(cooldown)
			dir := t.TempDir()
			path := filepath.Join(dir, "credential.json")
			metadata := map[string]any{"type": "codex", "access_token": "access-a", "refresh_token": "refresh-a", "account_id": "account-a", "email": "same@example.test"}
			if tc.siwc {
				metadata["use_siwc"] = true
				metadata["siwc"] = saveImportSIWC("account-a")
			}
			auth, err := manager.Register(ctx, &coreauth.Auth{ID: t.Name(), FileName: "credential.json", Provider: "codex", Status: coreauth.StatusActive, Metadata: metadata, Attributes: map[string]string{coreauth.AttributePath: path}})
			if err != nil {
				t.Fatal(err)
			}
			model := t.Name() + "-model"
			registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			retry := time.Hour
			manager.MarkResult(ctx, coreauth.Result{AuthID: auth.ID, Provider: "codex", Model: model, CredentialScope: true, Error: &coreauth.Error{HTTPStatus: 429, Message: `{"error":{"type":"usage_limit_reached"}}`}, RetryAfter: &retry})
			expected, _ := manager.GetByID(auth.ID)
			if !coreauth.QuotaRecoveryCandidate(expected) {
				t.Fatal("fixture is not recoverable before persistence")
			}
			disk := expected.Clone().Metadata
			tc.change(disk)
			encoded, err := json.Marshal(disk)
			if err != nil {
				t.Fatal(err)
			}
			// Another process commits the replacement before the manager's watcher imports it.
			if err = os.WriteFile(path, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			store := sdkauth.NewFileTokenStore()
			store.SetBaseDir(dir)
			manager.SetStore(store)
			recordsBefore, err := cooldown.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := manager.ResetQuotaIfUnchanged(ctx, expected)
			if err != nil {
				t.Fatal(err)
			}
			current, _ := manager.GetByID(auth.ID)
			recordsAfter, err := cooldown.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if tc.recover {
				if got == nil || current.Unavailable || current.Quota.Exceeded || len(recordsAfter) != 0 {
					t.Fatal("unchanged saved identity prevented quota recovery")
				}
				return
			}
			if got != nil || !reflect.DeepEqual(current, expected) || !reflect.DeepEqual(recordsAfter, recordsBefore) {
				t.Fatal("saved replacement credentials were published with inspected account cooldowns cleared")
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var persisted map[string]any
			if err = json.Unmarshal(raw, &persisted); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"access_token", "refresh_token", "account_id", "siwc", "use_siwc"} {
				if !reflect.DeepEqual(persisted[key], disk[key]) {
					t.Fatalf("rejected recovery rewrote replacement credential field %s", key)
				}
			}
			// The speculative save must not suppress a later ordinary edit or refresh.
			edit := current.Clone()
			edit.Metadata["after_rejected_recovery"] = "ordinary-edit"
			edited, err := manager.Update(ctx, edit)
			if err != nil {
				t.Fatal(err)
			}
			refreshed := edited.Clone()
			refreshed.Metadata["after_rejected_refresh"] = "refresh-edit"
			if _, err = manager.UpdateRefreshedAuth(ctx, edited, refreshed); err != nil {
				t.Fatal(err)
			}
			raw, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(raw, &persisted); err != nil {
				t.Fatal(err)
			}
			if persisted["after_rejected_recovery"] != "ordinary-edit" || persisted["after_rejected_refresh"] != "refresh-edit" {
				t.Fatal("aborted recovery watermark suppressed a later edit or refresh")
			}
		})
	}
}

func saveImportIDToken(account string) string {
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"`+account+`"}}`)) + ".sig"
}
func saveImportSIWC(account string) map[string]any {
	return map[string]any{"access_token": "siwc-access", "refresh_token": "siwc-refresh", "client_id": "siwc-client", "id_token": saveImportIDToken(account)}
}
