package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// validSessionIDPattern ensures session IDs are safe for filesystem operations
var validSessionIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

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

	// Ensure directory has secure permissions
	info, err := os.Stat(baseDir)
	if err != nil {
		return nil, fmt.Errorf("session filestore: stat directory failed: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("session filestore: %s is not a directory", baseDir)
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(baseDir, 0o700); err != nil {
			log.Warnf("Failed to enforce directory permissions on %s: %v", baseDir, err)
		}
	}

	return &FileStore{
		baseDir: baseDir,
	}, nil
}

// validateSessionID ensures the session ID is safe for filesystem operations
func validateSessionID(id string) error {
	if id == "" {
		return fmt.Errorf("session ID is empty")
	}
	if !validSessionIDPattern.MatchString(id) {
		return fmt.Errorf("session ID contains invalid characters (must be alphanumeric, hyphens, or underscores)")
	}
	return nil
}

// Get retrieves a session by ID from disk.
// Returns nil if the session doesn't exist or has expired.
func (s *FileStore) Get(ctx context.Context, id string) (*Session, error) {
	id = strings.TrimSpace(id)
	if err := validateSessionID(id); err != nil {
		return nil, fmt.Errorf("session filestore: %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	path := filepath.Join(s.baseDir, id+".json")

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
	if err := validateSessionID(session.ID); err != nil {
		return "", fmt.Errorf("session filestore: %w", err)
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

	// Ensure temp file cleanup on error using defer
	defer func() {
		if _, err := os.Stat(tmp); err == nil {
			if removeErr := os.Remove(tmp); removeErr != nil {
				log.Warnf("Failed to clean up temp file %s: %v", tmp, removeErr)
			}
		}
	}()

	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("session filestore: rename failed: %w", err)
	}

	return session.ID, nil
}

// Delete removes a session file from disk.
// Returns nil if the session doesn't exist (idempotent).
func (s *FileStore) Delete(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if err := validateSessionID(id); err != nil {
		return fmt.Errorf("session filestore: %w", err)
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
	defer s.mu.RUnlock()

	baseDir := s.baseDir
	sessions := make([]*Session, 0)

	err := filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Propagate errors instead of silently ignoring them
			return walkErr
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
			// Skip files we can't read, but log the error
			log.Warnf("Failed to read session file %s: %v", path, err)
			return nil
		}

		if len(data) == 0 {
			return nil
		}

		var session Session
		if err := json.Unmarshal(data, &session); err != nil {
			// Skip malformed files, but log the error
			log.Warnf("Failed to unmarshal session file %s: %v", path, err)
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
			return walkErr
		}

		if d.IsDir() {
			return nil
		}

		if !strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			log.Warnf("Failed to read session file during cleanup %s: %v", path, err)
			return nil
		}

		if len(data) == 0 {
			return nil
		}

		var session Session
		if err := json.Unmarshal(data, &session); err != nil {
			log.Warnf("Failed to unmarshal session file during cleanup %s: %v", path, err)
			return nil
		}

		// Remove expired sessions
		if now.After(session.ExpiresAt) {
			if err := os.Remove(path); err == nil {
				count++
			} else {
				log.Warnf("Failed to remove expired session %s: %v", path, err)
			}
		}

		return nil
	})

	if err != nil {
		return count, fmt.Errorf("session filestore: cleanup failed: %w", err)
	}

	return count, nil
}
