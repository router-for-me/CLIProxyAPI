package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestFileTokenStorePersistMutationUnsetsPriorityExactly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "synthetic-auth.json")
	if err := os.WriteFile(path, []byte(`{"type":"codex","access_token":"synthetic-token","priority":10,"note":"keep","large_id":9007199254740993}`), 0o600); err != nil {
		t.Fatalf("seed auth file: %v", err)
	}

	store := NewFileTokenStore()
	store.SetBaseDir(dir)
	auths, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("List() len = %d, want 1", len(auths))
	}
	before := auths[0]
	after := before.Clone()
	delete(after.Metadata, "priority")
	delete(after.Attributes, "priority")

	if _, err = store.PersistMutation(context.Background(), before, after); err != nil {
		t.Fatalf("PersistMutation() error = %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	var persisted map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err = decoder.Decode(&persisted); err != nil {
		t.Fatalf("decode auth file: %v", err)
	}
	if _, ok := persisted["priority"]; ok {
		t.Fatalf("priority still present: %s", raw)
	}
	if got := persisted["access_token"]; got != "synthetic-token" {
		t.Fatalf("access_token = %#v, want preserved", got)
	}
	if got := persisted["note"]; got != "keep" {
		t.Fatalf("note = %#v, want preserved", got)
	}
	if got := persisted["large_id"]; got != json.Number("9007199254740993") {
		t.Fatalf("large_id = %#v, want exact numeric preservation", got)
	}
	if _, present := persisted["disabled"]; present {
		t.Fatalf("disabled field was added: %s", raw)
	}
}

func TestFileTokenStorePersistMutationRejectsReplacedSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "synthetic-auth.json")
	if err := os.WriteFile(path, []byte(`{"type":"codex","access_token":"old-synthetic-token","priority":10}`), 0o600); err != nil {
		t.Fatalf("seed auth file: %v", err)
	}
	store := NewFileTokenStore()
	store.SetBaseDir(dir)
	auths, err := store.List(context.Background())
	if err != nil || len(auths) != 1 {
		t.Fatalf("List() auths=%d error=%v", len(auths), err)
	}
	before := auths[0]
	after := before.Clone()
	after.Metadata["priority"] = 101
	after.Attributes["priority"] = "101"

	replacement := []byte(`{"type":"codex","access_token":"replacement-synthetic-token","priority":7}`)
	if err = os.WriteFile(path, replacement, 0o600); err != nil {
		t.Fatalf("replace auth file: %v", err)
	}
	if _, err = store.PersistMutation(context.Background(), before, after); !errors.Is(err, cliproxyauth.ErrAuthSourceConflict) {
		t.Fatalf("PersistMutation() error = %v, want ErrAuthSourceConflict", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read replacement: %v", err)
	}
	if string(got) != string(replacement) {
		t.Fatalf("replacement changed: got %s want %s", got, replacement)
	}
}

func TestFileTokenStorePersistMutationRejectsReplacementDuringPublish(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "synthetic-auth.json")
	if err := os.WriteFile(path, []byte(`{"type":"codex","access_token":"old-synthetic-token","priority":10}`), 0o600); err != nil {
		t.Fatalf("seed auth file: %v", err)
	}
	store := NewFileTokenStore()
	store.SetBaseDir(dir)
	auths, err := store.List(context.Background())
	if err != nil || len(auths) != 1 {
		t.Fatalf("List() auths=%d error=%v", len(auths), err)
	}
	after := auths[0].Clone()
	after.Metadata["priority"] = float64(101)
	after.Attributes["priority"] = "101"

	replacement := []byte(`{"type":"codex","access_token":"replacement-synthetic-token","priority":7}`)
	originalReplace := replaceAuthMutationFile
	replaceAuthMutationFile = func(tempPath, destinationPath string, expectedRaw []byte) error {
		if errWrite := os.WriteFile(destinationPath, replacement, 0o600); errWrite != nil {
			return errWrite
		}
		return originalReplace(tempPath, destinationPath, expectedRaw)
	}
	t.Cleanup(func() { replaceAuthMutationFile = originalReplace })

	if _, err = store.PersistMutation(context.Background(), auths[0], after); !errors.Is(err, cliproxyauth.ErrAuthSourceConflict) {
		t.Fatalf("PersistMutation() error = %v, want ErrAuthSourceConflict", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read replacement: %v", err)
	}
	if string(got) != string(replacement) {
		t.Fatalf("replacement changed: got %s want %s", got, replacement)
	}
}

func TestFileTokenStorePersistMutationPublishFailureLeavesOriginalBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "synthetic-auth.json")
	original := []byte(`{"type":"codex","access_token":"synthetic-token","priority":10}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("seed auth file: %v", err)
	}
	store := NewFileTokenStore()
	store.SetBaseDir(dir)
	auths, err := store.List(context.Background())
	if err != nil || len(auths) != 1 {
		t.Fatalf("List() auths=%d error=%v", len(auths), err)
	}
	after := auths[0].Clone()
	after.Metadata["priority"] = float64(101)
	after.Attributes["priority"] = "101"

	originalReplace := replaceAuthMutationFile
	replaceAuthMutationFile = func(string, string, []byte) error { return errors.New("synthetic rename failure") }
	t.Cleanup(func() { replaceAuthMutationFile = originalReplace })
	if _, err = store.PersistMutation(context.Background(), auths[0], after); err == nil {
		t.Fatal("PersistMutation() error = nil, want publish failure")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("original bytes changed: got %s want %s", got, original)
	}
}

func TestFileTokenStoreRejectsStalePersistedWatcherSnapshotAfterPriorityMutation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "synthetic-auth.json")
	if err := os.WriteFile(path, []byte(`{"type":"codex","access_token":"synthetic-token","priority":10}`), 0o600); err != nil {
		t.Fatalf("seed auth file: %v", err)
	}
	store := NewFileTokenStore()
	store.SetBaseDir(dir)
	manager := cliproxyauth.NewManager(store, &cliproxyauth.FillFirstSelector{}, nil)
	if err := manager.Load(context.Background()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	staleSnapshots, err := store.List(context.Background())
	if err != nil || len(staleSnapshots) != 1 {
		t.Fatalf("List() auths=%d error=%v", len(staleSnapshots), err)
	}
	registered, _ := manager.GetByID("synthetic-auth.json")
	result, err := manager.MutatePriority(context.Background(), registered.ID, registered.Revision(), cliproxyauth.PriorityMutation{
		Operation: cliproxyauth.PriorityMutationSet,
		Priority:  101,
	})
	if err != nil {
		t.Fatalf("MutatePriority() error = %v", err)
	}
	if _, err = manager.Update(cliproxyauth.WithSkipPersist(context.Background()), staleSnapshots[0]); !errors.Is(err, cliproxyauth.ErrAuthSourceConflict) {
		t.Fatalf("stale watcher Update() error = %v, want ErrAuthSourceConflict", err)
	}
	current, _ := manager.GetByID(registered.ID)
	if current.Revision() != result.Revision || current.Attributes["priority"] != "101" {
		t.Fatalf("stale watcher rolled back runtime: revision=%q priority=%q", current.Revision(), current.Attributes["priority"])
	}
}
