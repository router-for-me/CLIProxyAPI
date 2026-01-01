package session

import (
	"context"
	"sync"
	"testing"
	"time"
)

// mockStore implements Store interface for testing
type mockStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func newMockStore() *mockStore {
	return &mockStore{
		sessions: make(map[string]*Session),
	}
}

func (m *mockStore) Get(ctx context.Context, id string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if s, ok := m.sessions[id]; ok {
		if s.IsExpired() {
			return nil, nil
		}
		// Return a copy to simulate real storage
		copy := *s
		return &copy, nil
	}
	return nil, nil
}

func (m *mockStore) Save(ctx context.Context, session *Session) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[session.ID] = session
	return session.ID, nil
}

func (m *mockStore) Delete(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, id)
	return nil
}

func (m *mockStore) List(ctx context.Context) ([]*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		if !s.IsExpired() {
			result = append(result, s)
		}
	}
	return result, nil
}

func (m *mockStore) Cleanup(ctx context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for id, s := range m.sessions {
		if s.IsExpired() {
			delete(m.sessions, id)
			count++
		}
	}
	return count, nil
}

func TestNewManager(t *testing.T) {
	t.Run("creates manager with defaults", func(t *testing.T) {
		store := newMockStore()
		mgr, err := NewManager(Config{
			Store:           store,
			CleanupInterval: -1, // Disable automatic cleanup for test
		})
		if err != nil {
			t.Fatalf("NewManager() error = %v", err)
		}
		defer mgr.StopCleanup()

		if mgr == nil {
			t.Fatal("NewManager() returned nil")
		}
		if mgr.defaultTTL != 24*time.Hour {
			t.Errorf("defaultTTL = %v, want %v", mgr.defaultTTL, 24*time.Hour)
		}
	})

	t.Run("rejects nil store", func(t *testing.T) {
		_, err := NewManager(Config{
			Store: nil,
		})
		if err == nil {
			t.Error("NewManager() should reject nil store")
		}
	})

	t.Run("accepts custom TTL", func(t *testing.T) {
		store := newMockStore()
		customTTL := 12 * time.Hour
		mgr, err := NewManager(Config{
			Store:           store,
			DefaultTTL:      customTTL,
			CleanupInterval: -1,
		})
		if err != nil {
			t.Fatalf("NewManager() error = %v", err)
		}
		defer mgr.StopCleanup()

		if mgr.defaultTTL != customTTL {
			t.Errorf("defaultTTL = %v, want %v", mgr.defaultTTL, customTTL)
		}
	})
}

func TestManager_Create(t *testing.T) {
	store := newMockStore()
	mgr := createTestManager(t, store)
	ctx := context.Background()

	t.Run("creates session with UUID", func(t *testing.T) {
		session, err := mgr.Create(ctx)
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if session == nil {
			t.Fatal("Create() returned nil session")
		}
		if session.ID == "" {
			t.Error("Create() session ID is empty")
		}
		if len(session.ID) < 32 {
			t.Error("Create() session ID doesn't look like a UUID")
		}
		if session.IsExpired() {
			t.Error("newly created session should not be expired")
		}
	})

	t.Run("session is persisted", func(t *testing.T) {
		session, err := mgr.Create(ctx)
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}

		retrieved, err := mgr.Get(ctx, session.ID)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if retrieved == nil {
			t.Error("created session should be retrievable")
		}
		if retrieved.ID != session.ID {
			t.Errorf("retrieved ID = %v, want %v", retrieved.ID, session.ID)
		}
	})
}

func TestManager_GetOrCreate(t *testing.T) {
	store := newMockStore()
	mgr := createTestManager(t, store)
	ctx := context.Background()

	t.Run("creates new session with custom ID", func(t *testing.T) {
		customID := "my-custom-session-id"
		session, err := mgr.GetOrCreate(ctx, customID)
		if err != nil {
			t.Fatalf("GetOrCreate() error = %v", err)
		}
		if session == nil {
			t.Fatal("GetOrCreate() returned nil")
		}
		if session.ID != customID {
			t.Errorf("session ID = %v, want %v", session.ID, customID)
		}
	})

	t.Run("returns existing session", func(t *testing.T) {
		// Create initial session
		initial, err := mgr.GetOrCreate(ctx, "existing-session")
		if err != nil {
			t.Fatalf("GetOrCreate() error = %v", err)
		}

		// Add a message to identify it
		initial.AddMessage(Message{
			Role:    "user",
			Content: "test message",
		})
		if err := mgr.Update(ctx, initial); err != nil {
			t.Fatalf("Update() error = %v", err)
		}

		// Get the same session
		retrieved, err := mgr.GetOrCreate(ctx, "existing-session")
		if err != nil {
			t.Fatalf("GetOrCreate() error = %v", err)
		}
		if retrieved == nil {
			t.Fatal("GetOrCreate() returned nil")
		}
		if len(retrieved.Messages) != 1 {
			t.Errorf("messages count = %d, want 1", len(retrieved.Messages))
		}
	})

	t.Run("creates new session when empty ID provided", func(t *testing.T) {
		session, err := mgr.GetOrCreate(ctx, "")
		if err != nil {
			t.Fatalf("GetOrCreate() error = %v", err)
		}
		if session == nil {
			t.Fatal("GetOrCreate() returned nil")
		}
		if session.ID == "" {
			t.Error("should create session with generated ID")
		}
	})

	t.Run("creates new session when expired", func(t *testing.T) {
		// Create and expire a session
		expired := &Session{
			ID:        "expired-getorcreate",
			CreatedAt: time.Now().Add(-2 * time.Hour),
			UpdatedAt: time.Now().Add(-2 * time.Hour),
			ExpiresAt: time.Now().Add(-1 * time.Hour),
			Messages: []Message{{
				Role:    "user",
				Content: "old message",
			}},
			Metadata: SessionMetadata{},
		}
		store.sessions["expired-getorcreate"] = expired

		// GetOrCreate should create a fresh session
		session, err := mgr.GetOrCreate(ctx, "expired-getorcreate")
		if err != nil {
			t.Fatalf("GetOrCreate() error = %v", err)
		}
		if session == nil {
			t.Fatal("GetOrCreate() returned nil")
		}
		if len(session.Messages) != 0 {
			t.Error("should create fresh session without old messages")
		}
		if session.IsExpired() {
			t.Error("new session should not be expired")
		}
	})
}

func TestManager_AppendMessage(t *testing.T) {
	store := newMockStore()
	mgr := createTestManager(t, store)
	ctx := context.Background()

	t.Run("appends message to session", func(t *testing.T) {
		session, _ := mgr.Create(ctx)

		msg := Message{
			Role:      "user",
			Content:   "Hello!",
			Provider:  "openai",
			Model:     "gpt-4",
			Timestamp: time.Now(),
		}

		if err := mgr.AppendMessage(ctx, session.ID, msg); err != nil {
			t.Fatalf("AppendMessage() error = %v", err)
		}

		retrieved, _ := mgr.Get(ctx, session.ID)
		if len(retrieved.Messages) != 1 {
			t.Errorf("messages count = %d, want 1", len(retrieved.Messages))
		}
		if retrieved.Messages[0].Content != "Hello!" {
			t.Error("message content mismatch")
		}
	})

	t.Run("rejects empty session ID", func(t *testing.T) {
		msg := Message{Role: "user", Content: "test"}
		err := mgr.AppendMessage(ctx, "", msg)
		if err == nil {
			t.Error("should reject empty session ID")
		}
	})

	t.Run("rejects non-existent session", func(t *testing.T) {
		msg := Message{Role: "user", Content: "test"}
		err := mgr.AppendMessage(ctx, "non-existent", msg)
		if err == nil {
			t.Error("should reject non-existent session")
		}
	})
}

func TestManager_Delete(t *testing.T) {
	store := newMockStore()
	mgr := createTestManager(t, store)
	ctx := context.Background()

	t.Run("deletes session", func(t *testing.T) {
		session, _ := mgr.Create(ctx)

		if err := mgr.Delete(ctx, session.ID); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}

		retrieved, _ := mgr.Get(ctx, session.ID)
		if retrieved != nil {
			t.Error("session should be deleted")
		}
	})
}

func TestManager_List(t *testing.T) {
	store := newMockStore()
	mgr := createTestManager(t, store)
	ctx := context.Background()

	t.Run("lists all sessions", func(t *testing.T) {
		// Create multiple sessions
		for i := 0; i < 3; i++ {
			if _, err := mgr.Create(ctx); err != nil {
				t.Fatalf("Create() error = %v", err)
			}
		}

		sessions, err := mgr.List(ctx)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(sessions) < 3 {
			t.Errorf("List() returned %d sessions, want at least 3", len(sessions))
		}
	})
}

func TestManager_Cleanup(t *testing.T) {
	store := newMockStore()
	mgr := createTestManager(t, store)
	ctx := context.Background()

	t.Run("removes expired sessions", func(t *testing.T) {
		// Add expired session directly to store
		expired := &Session{
			ID:        "cleanup-test-expired",
			CreatedAt: time.Now().Add(-2 * time.Hour),
			UpdatedAt: time.Now().Add(-2 * time.Hour),
			ExpiresAt: time.Now().Add(-1 * time.Hour),
			Messages:  []Message{},
			Metadata:  SessionMetadata{},
		}
		store.sessions["cleanup-test-expired"] = expired

		// Add active session
		active, _ := mgr.Create(ctx)

		count, err := mgr.Cleanup(ctx)
		if err != nil {
			t.Fatalf("Cleanup() error = %v", err)
		}
		if count < 1 {
			t.Errorf("Cleanup() count = %d, want at least 1", count)
		}

		// Verify expired is gone
		if _, ok := store.sessions["cleanup-test-expired"]; ok {
			t.Error("expired session should be removed")
		}

		// Verify active still exists
		retrieved, _ := mgr.Get(ctx, active.ID)
		if retrieved == nil {
			t.Error("active session should still exist")
		}
	})
}

func TestManager_ExtendTTL(t *testing.T) {
	store := newMockStore()
	mgr := createTestManager(t, store)
	ctx := context.Background()

	t.Run("extends session TTL", func(t *testing.T) {
		session, _ := mgr.Create(ctx)
		originalExpiry := session.ExpiresAt

		extension := 48 * time.Hour
		if err := mgr.ExtendTTL(ctx, session.ID, extension); err != nil {
			t.Fatalf("ExtendTTL() error = %v", err)
		}

		retrieved, _ := mgr.Get(ctx, session.ID)
		if !retrieved.ExpiresAt.After(originalExpiry) {
			t.Error("expiry should be extended")
		}
	})

	t.Run("rejects non-existent session", func(t *testing.T) {
		err := mgr.ExtendTTL(ctx, "non-existent", time.Hour)
		if err == nil {
			t.Error("should reject non-existent session")
		}
	})
}

func TestManager_StopCleanup(t *testing.T) {
	t.Run("stop cleanup is idempotent", func(t *testing.T) {
		store := newMockStore()
		mgr, err := NewManager(Config{
			Store:           store,
			CleanupInterval: 100 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("NewManager() error = %v", err)
		}

		// Stop multiple times - should not panic
		mgr.StopCleanup()
		mgr.StopCleanup()
		mgr.StopCleanup()
	})

	t.Run("cleanup can be restarted", func(t *testing.T) {
		store := newMockStore()
		mgr, err := NewManager(Config{
			Store:           store,
			CleanupInterval: -1, // Start without cleanup
		})
		if err != nil {
			t.Fatalf("NewManager() error = %v", err)
		}

		// Start, stop, start again
		mgr.StartCleanup(100 * time.Millisecond)
		mgr.StopCleanup()
		mgr.StartCleanup(100 * time.Millisecond)
		mgr.StopCleanup()
	})
}

func TestManager_Update(t *testing.T) {
	store := newMockStore()
	mgr := createTestManager(t, store)
	ctx := context.Background()

	t.Run("updates session", func(t *testing.T) {
		session, _ := mgr.Create(ctx)
		originalUpdatedAt := session.UpdatedAt

		// Wait a moment to ensure time difference
		time.Sleep(10 * time.Millisecond)

		session.Messages = append(session.Messages, Message{
			Role:    "user",
			Content: "updated",
		})

		if err := mgr.Update(ctx, session); err != nil {
			t.Fatalf("Update() error = %v", err)
		}

		retrieved, _ := mgr.Get(ctx, session.ID)
		if len(retrieved.Messages) != 1 {
			t.Error("messages should be updated")
		}
		if !retrieved.UpdatedAt.After(originalUpdatedAt) {
			t.Error("UpdatedAt should be updated")
		}
	})

	t.Run("rejects nil session", func(t *testing.T) {
		err := mgr.Update(ctx, nil)
		if err == nil {
			t.Error("should reject nil session")
		}
	})
}

// Helper function to create a test manager
func createTestManager(t *testing.T, store Store) *Manager {
	t.Helper()
	mgr, err := NewManager(Config{
		Store:           store,
		CleanupInterval: -1, // Disable automatic cleanup for tests
	})
	if err != nil {
		t.Fatalf("failed to create test manager: %v", err)
	}
	t.Cleanup(func() {
		mgr.StopCleanup()
	})
	return mgr
}
