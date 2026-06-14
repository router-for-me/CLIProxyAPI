package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteAuthMetadataFile_WritesAtomicallyWithoutTempLeftover(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codex.json")
	// Seed an existing file to ensure it is replaced, not appended to.
	if err := os.WriteFile(path, []byte(`{"old":true}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	meta := map[string]any{"type": "codex", "access_token": "tok"}
	if err := writeAuthMetadataFile(path, meta); err != nil {
		t.Fatalf("writeAuthMetadataFile: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal %q: %v", string(raw), err)
	}
	if got["access_token"] != "tok" || got["type"] != "codex" {
		t.Fatalf("unexpected content: %s", string(raw))
	}
	if _, stale := got["old"]; stale {
		t.Fatalf("old content not replaced: %s", string(raw))
	}

	// The atomic rename must not leave the sibling temp file behind.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file should not remain after atomic write (err=%v)", err)
	}
}

func TestList_HonorsContextCancellationDuringCodexEnrichment(t *testing.T) {
	dir := t.TempDir()
	// A codex auth with an expired subscription forces the backend enrichment
	// path (which would otherwise wait up to 20s on a slow/unreachable host).
	seed := `{"type":"codex","access_token":"a","id_token":"","subscription_active_until":"2000-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, "codex.json"), []byte(seed), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	store := NewFileTokenStore()
	store.SetBaseDir(dir)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	done := make(chan struct{})
	go func() {
		_, _ = store.List(ctx)
		close(done)
	}()

	select {
	case <-done:
		// Returned promptly: the enrichment honored the cancelled context.
	case <-time.After(5 * time.Second):
		t.Fatal("List did not return promptly under a cancelled context (enrichment ignored ctx)")
	}
}

func TestList_CopiesCodexPlanTypeIntoAttributes(t *testing.T) {
	dir := t.TempDir()
	// Future expiry keeps enrichment offline (no backend call) while still
	// exercising the attribute copy.
	future := time.Now().UTC().Add(30 * 24 * time.Hour).Format("2006-01-02T15:04:05Z")
	seed := `{"type":"codex","access_token":"a","plan_type":"plus","subscription_active_until":"` + future + `"}`
	if err := os.WriteFile(filepath.Join(dir, "codex.json"), []byte(seed), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	store := NewFileTokenStore()
	store.SetBaseDir(dir)
	auths, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("want 1 auth, got %d", len(auths))
	}
	if got := auths[0].Attributes["plan_type"]; got != "plus" {
		t.Fatalf("Attributes[plan_type] = %q, want plus (runtime catalog selection)", got)
	}
	if got := auths[0].Attributes["subscription_active_until"]; got != future {
		t.Fatalf("Attributes[subscription_active_until] = %q, want %s", got, future)
	}
}

func TestPersistCodexSubscriptionFields_DoesNotClobberTokens(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codex.json")
	// Simulate the latest on-disk state written by a concurrent token Save.
	if err := os.WriteFile(path, []byte(`{"type":"codex","access_token":"fresh","refresh_token":"fresh-r"}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	store := NewFileTokenStore()
	store.SetBaseDir(dir)
	// Enrichment built from a STALE read (old tokens) plus new subscription info.
	enriched := map[string]any{
		"access_token":              "stale",
		"refresh_token":             "stale-r",
		"plan_type":                 "plus",
		"subscription_active_until": "2030-01-01T00:00:00Z",
		"subscription_expired":      false,
	}
	store.persistCodexSubscriptionFields(path, enriched)

	raw, _ := os.ReadFile(path)
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal %q: %v", string(raw), err)
	}
	// Tokens must remain the fresh on-disk values, not be rolled back.
	if got["access_token"] != "fresh" || got["refresh_token"] != "fresh-r" {
		t.Fatalf("tokens were clobbered: %s", string(raw))
	}
	// Subscription fields must be written.
	if got["plan_type"] != "plus" {
		t.Fatalf("plan_type not persisted: %s", string(raw))
	}
	if got["subscription_active_until"] != "2030-01-01T00:00:00Z" {
		t.Fatalf("expiry not persisted: %s", string(raw))
	}
}
