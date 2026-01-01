package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestNewFileStore(t *testing.T) {
	t.Run("creates directory if not exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		storeDir := filepath.Join(tmpDir, "sessions")

		store, err := NewFileStore(storeDir)
		if err != nil {
			t.Fatalf("NewFileStore() error = %v", err)
		}
		if store == nil {
			t.Fatal("NewFileStore() returned nil")
		}

		info, err := os.Stat(storeDir)
		if err != nil {
			t.Fatalf("directory not created: %v", err)
		}
		if !info.IsDir() {
			t.Error("expected directory, got file")
		}
		if info.Mode().Perm() != 0o700 {
			t.Errorf("expected permissions 0700, got %o", info.Mode().Perm())
		}
	})

	t.Run("rejects empty baseDir", func(t *testing.T) {
		_, err := NewFileStore("")
		if err == nil {
			t.Error("expected error for empty baseDir")
		}
	})

	t.Run("rejects non-directory path", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "notadir")
		if err := os.WriteFile(filePath, []byte("test"), 0o600); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}

		_, err := NewFileStore(filePath)
		if err == nil {
			t.Error("expected error for non-directory path")
		}
	})
}

func TestValidateSessionID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"valid alphanumeric", "abc123", false},
		{"valid with hyphen", "session-123", false},
		{"valid with underscore", "session_123", false},
		{"valid UUID format", "550e8400-e29b-41d4-a716-446655440000", false},
		{"empty string", "", true},
		{"path traversal attempt", "../secret", true},
		{"absolute path", "/etc/passwd", true},
		{"special chars", "session@123", true},
		{"spaces", "session 123", true},
		{"dots only", "...", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSessionID(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateSessionID(%q) error = %v, wantErr %v", tt.id, err, tt.wantErr)
			}
		})
	}
}

func TestFileStore_SaveAndGet(t *testing.T) {
	store := createTestStore(t)
	ctx := context.Background()

	t.Run("save and retrieve session", func(t *testing.T) {
		session := createTestSession("test-session-1")

		id, err := store.Save(ctx, session)
		if err != nil {
			t.Fatalf("Save() error = %v", err)
		}
		if id != session.ID {
			t.Errorf("Save() id = %v, want %v", id, session.ID)
		}

		retrieved, err := store.Get(ctx, session.ID)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if retrieved == nil {
			t.Fatal("Get() returned nil")
		}
		if retrieved.ID != session.ID {
			t.Errorf("retrieved ID = %v, want %v", retrieved.ID, session.ID)
		}
		if len(retrieved.Messages) != len(session.Messages) {
			t.Errorf("retrieved messages count = %d, want %d", len(retrieved.Messages), len(session.Messages))
		}
	})

	t.Run("get non-existent session returns nil", func(t *testing.T) {
		retrieved, err := store.Get(ctx, "non-existent-id")
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if retrieved != nil {
			t.Error("Get() should return nil for non-existent session")
		}
	})

	t.Run("get expired session returns nil", func(t *testing.T) {
		session := &Session{
			ID:        "expired-session",
			CreatedAt: time.Now().Add(-2 * time.Hour),
			UpdatedAt: time.Now().Add(-2 * time.Hour),
			ExpiresAt: time.Now().Add(-1 * time.Hour), // Expired 1 hour ago
			Messages:  []Message{},
			Metadata:  SessionMetadata{},
		}

		if _, err := store.Save(ctx, session); err != nil {
			t.Fatalf("Save() error = %v", err)
		}

		retrieved, err := store.Get(ctx, session.ID)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if retrieved != nil {
			t.Error("Get() should return nil for expired session")
		}
	})

	t.Run("rejects nil session", func(t *testing.T) {
		_, err := store.Save(ctx, nil)
		if err == nil {
			t.Error("Save() should reject nil session")
		}
	})

	t.Run("rejects invalid session ID", func(t *testing.T) {
		session := &Session{
			ID:        "../invalid",
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(time.Hour),
		}
		_, err := store.Save(ctx, session)
		if err == nil {
			t.Error("Save() should reject invalid session ID")
		}
	})

	t.Run("get rejects invalid session ID", func(t *testing.T) {
		_, err := store.Get(ctx, "../invalid")
		if err == nil {
			t.Error("Get() should reject invalid session ID")
		}
	})

	t.Run("trims whitespace from ID", func(t *testing.T) {
		session := createTestSession("whitespace-test")
		if _, err := store.Save(ctx, session); err != nil {
			t.Fatalf("Save() error = %v", err)
		}

		retrieved, err := store.Get(ctx, "  whitespace-test  ")
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if retrieved == nil {
			t.Error("Get() should trim whitespace and find session")
		}
	})
}

func TestFileStore_Delete(t *testing.T) {
	store := createTestStore(t)
	ctx := context.Background()

	t.Run("delete existing session", func(t *testing.T) {
		session := createTestSession("delete-test")
		if _, err := store.Save(ctx, session); err != nil {
			t.Fatalf("Save() error = %v", err)
		}

		if err := store.Delete(ctx, session.ID); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}

		retrieved, err := store.Get(ctx, session.ID)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if retrieved != nil {
			t.Error("session should be deleted")
		}
	})

	t.Run("delete non-existent session is idempotent", func(t *testing.T) {
		err := store.Delete(ctx, "non-existent")
		if err != nil {
			t.Errorf("Delete() should be idempotent, got error = %v", err)
		}
	})

	t.Run("delete rejects invalid session ID", func(t *testing.T) {
		err := store.Delete(ctx, "../invalid")
		if err == nil {
			t.Error("Delete() should reject invalid session ID")
		}
	})
}

func TestFileStore_List(t *testing.T) {
	store := createTestStore(t)
	ctx := context.Background()

	t.Run("list returns all active sessions", func(t *testing.T) {
		// Create multiple sessions
		for i := 0; i < 3; i++ {
			session := createTestSession("list-test-" + string(rune('a'+i)))
			if _, err := store.Save(ctx, session); err != nil {
				t.Fatalf("Save() error = %v", err)
			}
		}

		sessions, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(sessions) < 3 {
			t.Errorf("List() returned %d sessions, want at least 3", len(sessions))
		}
	})

	t.Run("list excludes expired sessions", func(t *testing.T) {
		// Create an expired session
		expired := &Session{
			ID:        "list-expired",
			CreatedAt: time.Now().Add(-2 * time.Hour),
			UpdatedAt: time.Now().Add(-2 * time.Hour),
			ExpiresAt: time.Now().Add(-1 * time.Hour),
			Messages:  []Message{},
			Metadata:  SessionMetadata{},
		}
		if _, err := store.Save(ctx, expired); err != nil {
			t.Fatalf("Save() error = %v", err)
		}

		sessions, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}

		for _, s := range sessions {
			if s.ID == "list-expired" {
				t.Error("List() should not include expired sessions")
			}
		}
	})
}

func TestFileStore_Cleanup(t *testing.T) {
	store := createTestStore(t)
	ctx := context.Background()

	t.Run("cleanup removes expired sessions", func(t *testing.T) {
		// Create an expired session
		expired := &Session{
			ID:        "cleanup-expired",
			CreatedAt: time.Now().Add(-2 * time.Hour),
			UpdatedAt: time.Now().Add(-2 * time.Hour),
			ExpiresAt: time.Now().Add(-1 * time.Hour),
			Messages:  []Message{},
			Metadata:  SessionMetadata{},
		}
		if _, err := store.Save(ctx, expired); err != nil {
			t.Fatalf("Save() error = %v", err)
		}

		// Create an active session
		active := createTestSession("cleanup-active")
		if _, err := store.Save(ctx, active); err != nil {
			t.Fatalf("Save() error = %v", err)
		}

		count, err := store.Cleanup(ctx)
		if err != nil {
			t.Fatalf("Cleanup() error = %v", err)
		}
		if count < 1 {
			t.Errorf("Cleanup() count = %d, want at least 1", count)
		}

		// Verify expired session is removed (check file directly since Get filters expired)
		path := filepath.Join(store.baseDir, "cleanup-expired.json")
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Error("expired session file should be removed")
		}

		// Verify active session still exists
		retrieved, err := store.Get(ctx, "cleanup-active")
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if retrieved == nil {
			t.Error("active session should still exist after cleanup")
		}
	})
}

func TestFileStore_AtomicWrites(t *testing.T) {
	store := createTestStore(t)
	ctx := context.Background()

	t.Run("no temp files left after save", func(t *testing.T) {
		session := createTestSession("atomic-test")
		if _, err := store.Save(ctx, session); err != nil {
			t.Fatalf("Save() error = %v", err)
		}

		// Check for leftover temp files
		files, err := os.ReadDir(store.baseDir)
		if err != nil {
			t.Fatalf("ReadDir() error = %v", err)
		}

		for _, f := range files {
			if filepath.Ext(f.Name()) == ".tmp" {
				t.Errorf("temp file left behind: %s", f.Name())
			}
		}
	})

	t.Run("file contains valid JSON", func(t *testing.T) {
		session := createTestSession("json-valid-test")
		session.Messages = append(session.Messages, Message{
			Role:      "user",
			Content:   "Hello, world!",
			Provider:  "openai",
			Model:     "gpt-4",
			Timestamp: time.Now(),
		})
		if _, err := store.Save(ctx, session); err != nil {
			t.Fatalf("Save() error = %v", err)
		}

		path := filepath.Join(store.baseDir, "json-valid-test.json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}

		var parsed Session
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Errorf("saved file is not valid JSON: %v", err)
		}
		if parsed.ID != session.ID {
			t.Errorf("parsed ID = %v, want %v", parsed.ID, session.ID)
		}
	})
}

func TestFileStore_ConcurrentAccess(t *testing.T) {
	store := createTestStore(t)
	ctx := context.Background()

	t.Run("concurrent saves are safe", func(t *testing.T) {
		var wg sync.WaitGroup
		errors := make(chan error, 10)

		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				session := createTestSession("concurrent-" + string(rune('0'+idx)))
				if _, err := store.Save(ctx, session); err != nil {
					errors <- err
				}
			}(i)
		}

		wg.Wait()
		close(errors)

		for err := range errors {
			t.Errorf("concurrent save error: %v", err)
		}
	})

	t.Run("concurrent reads are safe", func(t *testing.T) {
		// Create a session first
		session := createTestSession("concurrent-read")
		if _, err := store.Save(ctx, session); err != nil {
			t.Fatalf("Save() error = %v", err)
		}

		var wg sync.WaitGroup
		errors := make(chan error, 20)

		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := store.Get(ctx, "concurrent-read"); err != nil {
					errors <- err
				}
			}()
		}

		wg.Wait()
		close(errors)

		for err := range errors {
			t.Errorf("concurrent read error: %v", err)
		}
	})
}

// Helper functions

func createTestStore(t *testing.T) *FileStore {
	t.Helper()
	tmpDir := t.TempDir()
	store, err := NewFileStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	return store
}

func createTestSession(id string) *Session {
	now := time.Now()
	return &Session{
		ID:        id,
		CreatedAt: now,
		UpdatedAt: now,
		ExpiresAt: now.Add(24 * time.Hour),
		Messages:  []Message{},
		Metadata:  SessionMetadata{},
	}
}
