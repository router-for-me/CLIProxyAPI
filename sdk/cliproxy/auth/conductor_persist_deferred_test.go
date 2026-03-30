package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type persistCaptureStore struct {
	lastSaved *Auth
	saveCount int
}

func (s *persistCaptureStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *persistCaptureStore) Save(_ context.Context, auth *Auth) (string, error) {
	s.saveCount++
	s.lastSaved = auth.Clone()
	return "", nil
}

func (s *persistCaptureStore) Delete(context.Context, string) error { return nil }

func TestManagerUpdate_HydratesDeferredMetadataBeforePersist(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "persist-hydrate.json")
	fileMetadata := map[string]any{
		"type":            "claude",
		"email":           "persist@example.com",
		"access_token":    "token-from-file",
		"refresh_token":   "refresh-from-file",
		"custom_required": "preserve-me",
		"note":            "file-note",
	}
	raw, err := json.Marshal(fileMetadata)
	if err != nil {
		t.Fatalf("marshal file metadata: %v", err)
	}
	if err = os.WriteFile(authPath, raw, 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	store := &persistCaptureStore{}
	manager := NewManager(store, nil, nil)
	if _, err = manager.Register(context.Background(), &Auth{
		ID:       "persist-hydrate.json",
		FileName: "persist-hydrate.json",
		Provider: "claude",
		Status:   StatusActive,
		Attributes: map[string]string{
			"path": authPath,
		},
		Metadata:  fileMetadata,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	compactSnapshot, ok := manager.GetByID("persist-hydrate.json")
	if !ok || compactSnapshot == nil {
		t.Fatalf("expected compact snapshot")
	}
	if _, exists := compactSnapshot.Metadata["custom_required"]; exists {
		t.Fatalf("expected compact snapshot to omit non-allowlisted metadata")
	}
	compactSnapshot.Metadata["note"] = "updated-from-compact"
	compactSnapshot.Disabled = true

	if _, err = manager.Update(context.Background(), compactSnapshot); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	if store.lastSaved == nil {
		t.Fatalf("expected Save to be called")
	}
	if note, _ := store.lastSaved.Metadata["note"].(string); note != "updated-from-compact" {
		t.Fatalf("expected compact metadata override to persist, got %q", note)
	}
	if token, _ := store.lastSaved.Metadata["access_token"].(string); token != "token-from-file" {
		t.Fatalf("expected full metadata to be hydrated before persist, got token %q", token)
	}
	if marker, _ := store.lastSaved.Metadata["custom_required"].(string); marker != "preserve-me" {
		t.Fatalf("expected hydrated metadata to preserve required fields, got %q", marker)
	}
}

func TestManagerUpdate_DeferredPersistRespectsDeletedCompactKeys(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "persist-delete.json")
	fileMetadata := map[string]any{
		"type":          "claude",
		"email":         "delete@example.com",
		"access_token":  "token-from-file",
		"refresh_token": "refresh-from-file",
		"note":          "old-note",
		"priority":      9,
	}
	raw, err := json.Marshal(fileMetadata)
	if err != nil {
		t.Fatalf("marshal file metadata: %v", err)
	}
	if err = os.WriteFile(authPath, raw, 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	store := &persistCaptureStore{}
	manager := NewManager(store, nil, nil)
	if _, err = manager.Register(context.Background(), &Auth{
		ID:       "persist-delete.json",
		FileName: "persist-delete.json",
		Provider: "claude",
		Status:   StatusActive,
		Attributes: map[string]string{
			"path": authPath,
		},
		Metadata:  fileMetadata,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	compactSnapshot, ok := manager.GetByID("persist-delete.json")
	if !ok || compactSnapshot == nil {
		t.Fatalf("expected compact snapshot")
	}
	if _, exists := compactSnapshot.Metadata["note"]; !exists {
		t.Fatalf("expected compact snapshot to include note before deletion")
	}
	if _, exists := compactSnapshot.Metadata["priority"]; !exists {
		t.Fatalf("expected compact snapshot to include priority before deletion")
	}

	delete(compactSnapshot.Metadata, "note")
	delete(compactSnapshot.Metadata, "priority")

	if _, err = manager.Update(context.Background(), compactSnapshot); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	if store.lastSaved == nil {
		t.Fatalf("expected Save to be called")
	}
	if _, exists := store.lastSaved.Metadata["note"]; exists {
		t.Fatalf("expected note to be deleted from persisted metadata")
	}
	if _, exists := store.lastSaved.Metadata["priority"]; exists {
		t.Fatalf("expected priority to be deleted from persisted metadata")
	}
	if token, _ := store.lastSaved.Metadata["access_token"].(string); token != "token-from-file" {
		t.Fatalf("expected oauth tokens to remain intact, got %q", token)
	}
}
