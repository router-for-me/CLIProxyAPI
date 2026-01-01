package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FileStore persists session records using the filesystem as backing storage.
// Thread-safe for concurrent access.
type FileStore struct {
	mu      sync.RWMutex
	baseDir string
}

// NewFileStore creates a session store that saves sessions to disk as JSON files.
// Each session is stored in a separate file named <session-id>.json.
func NewFileStore(baseDir string) (*FileStore, error) {
	if baseDir == "" {
		return nil, fmt.Errorf("session filestore: baseDir cannot be empty")
	}

	// Expand tilde to home directory
	if strings.HasPrefix(baseDir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("session filestore: expand home dir failed: %w", err)
		}
		baseDir = filepath.Join(home, baseDir[2:])
	}

	// Create directory if it doesn't exist
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		return nil, fmt.Errorf("session filestore: create directory failed: %w", err)
	}

	return &FileStore{
		baseDir: baseDir,
	}, nil
}

// Get retrieves a session by ID from disk.
// Returns nil if the session doesn't exist or has expired.
func (s *FileStore) Get(ctx context.Context, id string) (*Session, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("session filestore: id is empty")
	}

	s.mu.RLock()
	path := filepath.Join(s.baseDir, id+".json")
	s.mu.RUnlock()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // Session doesn't exist
		}
		return nil, fmt.Errorf("session filestore: read failed: %w", err)
	}

	if len(data) == 0 {
		return nil, nil
	}

	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("session filestore: unmarshal failed: %w", err)
	}

	// Return nil for expired sessions
	if session.IsExpired() {
		return nil, nil
	}

	return &session, nil
}

// Save persists a session to disk.
// Creates a new file if the session doesn't exist, updates if it does.
func (s *FileStore) Save(ctx context.Context, session *Session) (string, error) {
	if session == nil {
		return "", fmt.Errorf("session filestore: session is nil")
	}
	if session.ID == "" {
		return "", fmt.Errorf("session filestore: session ID is empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.baseDir, session.ID+".json")

	// Marshal session to JSON
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return "", fmt.Errorf("session filestore: marshal failed: %w", err)
	}

	// Write to temp file first, then atomic rename
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", fmt.Errorf("session filestore: write temp failed: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) // Clean up temp file on error
		return "", fmt.Errorf("session filestore: rename failed: %w", err)
	}

	return session.ID, nil
}

// Delete removes a session file from disk.
// Returns nil if the session doesn't exist (idempotent).
func (s *FileStore) Delete(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("session filestore: id is empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.baseDir, id+".json")

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("session filestore: delete failed: %w", err)
	}

	return nil
}

// List returns all active (non-expired) sessions from disk.
func (s *FileStore) List(ctx context.Context) ([]*Session, error) {
	s.mu.RLock()
	baseDir := s.baseDir
	s.mu.RUnlock()

	sessions := make([]*Session, 0)

	err := filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Skip files we can't read
			return nil
		}

		if d.IsDir() {
			return nil
		}

		// Only process .json files
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			// Skip files we can't read
			return nil
		}

		if len(data) == 0 {
			return nil
		}

		var session Session
		if err := json.Unmarshal(data, &session); err != nil {
			// Skip malformed files
			return nil
		}

		// Only include non-expired sessions
		if !session.IsExpired() {
			sessions = append(sessions, &session)
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("session filestore: list failed: %w", err)
	}

	return sessions, nil
}

// Cleanup removes all expired sessions from disk.
// Returns the count of sessions purged.
func (s *FileStore) Cleanup(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	count := 0
	now := time.Now()

	err := filepath.WalkDir(s.baseDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}

		if d.IsDir() {
			return nil
		}

		if !strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		if len(data) == 0 {
			return nil
		}

		var session Session
		if err := json.Unmarshal(data, &session); err != nil {
			return nil
		}

		// Remove expired sessions
		if now.After(session.ExpiresAt) {
			if err := os.Remove(path); err == nil {
				count++
			}
		}

		return nil
	})

	if err != nil {
		return count, fmt.Errorf("session filestore: cleanup failed: %w", err)
	}

	return count, nil
}
