package session

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

// Manager handles session lifecycle operations including creation, retrieval,
// updates, and cleanup. Thread-safe for concurrent access.
type Manager struct {
	store          Store
	defaultTTL     time.Duration
	cleanupTicker  *time.Ticker
	cleanupStop    chan struct{}
	cleanupRunning bool
	mu             sync.RWMutex
}

// Config holds configuration for the session manager.
type Config struct {
	// Store is the backing storage for sessions.
	Store Store
	// DefaultTTL is the default session expiration duration (default: 24 hours).
	DefaultTTL time.Duration
	// CleanupInterval is how often to run expired session cleanup (default: 1 hour).
	// Set to 0 to disable automatic cleanup.
	CleanupInterval time.Duration
}

// NewManager creates a new session manager with the provided configuration.
func NewManager(cfg Config) (*Manager, error) {
	if cfg.Store == nil {
		return nil, fmt.Errorf("session manager: store is required")
	}

	if cfg.DefaultTTL == 0 {
		cfg.DefaultTTL = 24 * time.Hour
	}

	if cfg.CleanupInterval == 0 {
		cfg.CleanupInterval = 1 * time.Hour
	}

	m := &Manager{
		store:      cfg.Store,
		defaultTTL: cfg.DefaultTTL,
	}

	// Start automatic cleanup if interval is positive
	if cfg.CleanupInterval > 0 {
		m.StartCleanup(cfg.CleanupInterval)
	}

	return m, nil
}

// Create creates a new session with a generated UUID and default TTL.
func (m *Manager) Create(ctx context.Context) (*Session, error) {
	sessionID := uuid.New().String()
	now := time.Now()

	session := &Session{
		ID:        sessionID,
		CreatedAt: now,
		UpdatedAt: now,
		ExpiresAt: now.Add(m.defaultTTL),
		Messages:  []Message{},
		Metadata:  SessionMetadata{},
	}

	if _, err := m.store.Save(ctx, session); err != nil {
		return nil, fmt.Errorf("session manager: create failed: %w", err)
	}

	return session, nil
}

// Get retrieves a session by ID.
// Returns nil if the session doesn't exist or has expired.
func (m *Manager) Get(ctx context.Context, id string) (*Session, error) {
	if id == "" {
		return nil, nil
	}

	session, err := m.store.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("session manager: get failed: %w", err)
	}

	return session, nil
}

// GetOrCreate retrieves a session by ID, or creates a new one if it doesn't exist.
func (m *Manager) GetOrCreate(ctx context.Context, id string) (*Session, error) {
	// If no ID provided, create new session
	if id == "" {
		return m.Create(ctx)
	}

	// Try to get existing session
	session, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	// If session exists and is valid, return it
	if session != nil {
		return session, nil
	}

	// Session doesn't exist or expired, create new one with the requested ID
	now := time.Now()
	session = &Session{
		ID:        id,
		CreatedAt: now,
		UpdatedAt: now,
		ExpiresAt: now.Add(m.defaultTTL),
		Messages:  []Message{},
		Metadata:  SessionMetadata{},
	}

	if _, err := m.store.Save(ctx, session); err != nil {
		return nil, fmt.Errorf("session manager: create failed: %w", err)
	}

	return session, nil
}

// AppendMessage adds a message to an existing session and saves it.
func (m *Manager) AppendMessage(ctx context.Context, sessionID string, msg Message) error {
	if sessionID == "" {
		return fmt.Errorf("session manager: sessionID is empty")
	}

	session, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("session manager: get failed: %w", err)
	}

	if session == nil {
		return fmt.Errorf("session manager: session not found: %s", sessionID)
	}

	if session.IsExpired() {
		return fmt.Errorf("session manager: session expired: %s", sessionID)
	}

	session.AddMessage(msg)

	if _, err := m.store.Save(ctx, session); err != nil {
		return fmt.Errorf("session manager: save failed: %w", err)
	}

	return nil
}

// Update saves an updated session to the store.
func (m *Manager) Update(ctx context.Context, session *Session) error {
	if session == nil {
		return fmt.Errorf("session manager: session is nil")
	}

	session.Touch()

	if _, err := m.store.Save(ctx, session); err != nil {
		return fmt.Errorf("session manager: update failed: %w", err)
	}

	return nil
}

// Delete removes a session by ID.
func (m *Manager) Delete(ctx context.Context, id string) error {
	if err := m.store.Delete(ctx, id); err != nil {
		return fmt.Errorf("session manager: delete failed: %w", err)
	}

	return nil
}

// List returns all active sessions.
func (m *Manager) List(ctx context.Context) ([]*Session, error) {
	sessions, err := m.store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("session manager: list failed: %w", err)
	}

	return sessions, nil
}

// Cleanup removes all expired sessions.
// Returns the count of sessions purged.
func (m *Manager) Cleanup(ctx context.Context) (int, error) {
	count, err := m.store.Cleanup(ctx)
	if err != nil {
		return 0, fmt.Errorf("session manager: cleanup failed: %w", err)
	}

	return count, nil
}

// StartCleanup begins automatic periodic cleanup of expired sessions.
func (m *Manager) StartCleanup(interval time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cleanupRunning {
		return
	}

	m.cleanupTicker = time.NewTicker(interval)
	m.cleanupStop = make(chan struct{})
	m.cleanupRunning = true

	go func() {
		for {
			select {
			case <-m.cleanupTicker.C:
				// Run cleanup in background
				ctx := context.Background()
				if _, err := m.Cleanup(ctx); err != nil {
					log.WithError(err).Warn("Background session cleanup failed")
				}
			case <-m.cleanupStop:
				m.cleanupTicker.Stop()
				return
			}
		}
	}()
}

// StopCleanup halts automatic cleanup.
func (m *Manager) StopCleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.cleanupRunning {
		return
	}

	close(m.cleanupStop)
	m.cleanupRunning = false
}

// ExtendTTL extends a session's expiration by the specified duration.
func (m *Manager) ExtendTTL(ctx context.Context, sessionID string, duration time.Duration) error {
	session, err := m.Get(ctx, sessionID)
	if err != nil {
		return err
	}

	if session == nil {
		return fmt.Errorf("session manager: session not found: %s", sessionID)
	}

	// Extend from current time if session is expired, otherwise from current expiry
	base := session.ExpiresAt
	now := time.Now()
	if now.After(base) {
		base = now
	}
	session.ExpiresAt = base.Add(duration)
	session.Touch()

	if _, err := m.store.Save(ctx, session); err != nil {
		return fmt.Errorf("session manager: extend ttl failed: %w", err)
	}

	return nil
}
