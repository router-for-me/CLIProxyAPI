package auth

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadAuthFile_CodexRecoversPlanTypeFromIDToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codex-user.json")
	raw := map[string]any{
		"type":     "codex",
		"email":    "tester@example.com",
		"id_token": testCodexJWT(t, "plus"),
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal auth file: %v", err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	store := NewFileTokenStore()
	auth, err := store.readAuthFile(path, dir)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	if auth == nil {
		t.Fatal("expected auth")
	}
	if got := auth.Attributes["plan_type"]; got != "plus" {
		t.Fatalf("expected plan_type plus, got %q", got)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read updated auth file: %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(updated, &metadata); err != nil {
		t.Fatalf("unmarshal updated auth file: %v", err)
	}
	if got, _ := metadata["plan_type"].(string); got != "plus" {
		t.Fatalf("expected persisted plan_type plus, got %#v", metadata["plan_type"])
	}
}

func testCodexJWT(t *testing.T, planType string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payloadRaw, err := json.Marshal(map[string]any{
		"email": "tester@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct_123",
			"chatgpt_plan_type":  planType,
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(payloadRaw)
	return header + "." + payload + ".signature"
}
