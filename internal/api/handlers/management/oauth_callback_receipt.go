package management

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

// A callback receipt belongs to one registration, not just its reusable state
// string. Its gate never protects provider network calls or credential commits.
type oauthCallbackReceipt struct {
	mu       sync.Mutex
	closed   atomic.Bool
	state    string
	received bool
	consumed bool
	path     string
}

// closeCallbackLocked revokes admission before the store lock is released.
// Retaining the receipt until cleanup joins also fences same-state registration.
func (s *oauthSessionStore) closeCallbackLocked(receipt *oauthCallbackReceipt) *oauthCallbackReceipt {
	if receipt == nil {
		return nil
	}
	receipt.closed.Store(true)
	if s.retiredCallbacks == nil {
		s.retiredCallbacks = make(map[string]*oauthCallbackReceipt)
	}
	s.retiredCallbacks[receipt.state] = receipt
	return receipt
}

// cleanupCallbacks runs only after releasing store.mu. A terminal operation
// joins admitted file work before returning, without blocking other sessions'
// file operations behind the store lock. Removal failure cannot promise an
// absent file, but this retired generation never schedules a later deletion.
func (s *oauthSessionStore) cleanupCallbacks(receipts []*oauthCallbackReceipt) {
	for _, receipt := range receipts {
		if receipt == nil {
			continue
		}
		if s.beforeCallbackCleanup != nil {
			s.beforeCallbackCleanup()
		}
		receipt.mu.Lock()
		path := receipt.path
		receipt.path = ""
		removeFailed := false
		if path != "" {
			if errRemove := os.Remove(path); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
				removeFailed = true
			}
		}
		receipt.mu.Unlock()
		if removeFailed {
			log.Warn("failed to remove OAuth callback file")
		}
		s.mu.Lock()
		if s.retiredCallbacks[receipt.state] == receipt {
			delete(s.retiredCallbacks, receipt.state)
		}
		s.mu.Unlock()
	}
}

func (s *oauthSessionStore) callbackForPending(state, provider string) (*oauthCallbackReceipt, error) {
	state = strings.TrimSpace(state)
	provider = strings.ToLower(strings.TrimSpace(provider))
	s.mu.Lock()
	var cleanup []*oauthCallbackReceipt
	defer func() {
		s.mu.Unlock()
		s.cleanupCallbacks(cleanup)
	}()
	cleanup = s.purgeExpiredLocked(time.Now())
	session, ok := s.sessions[state]
	if !ok || session.Completed || session.Status != "" || session.Committing || session.SIWCCommitting || !strings.EqualFold(session.Provider, provider) {
		return nil, errOAuthSessionNotPending
	}
	if session.SIWC != nil {
		return nil, ErrSIWCCallbackRequiresV8
	}
	if session.callback == nil {
		session.callback = &oauthCallbackReceipt{state: state}
		s.sessions[state] = session
	}
	return session.callback, nil
}

// publishCallback keeps admission and atomic publication within one receipt.
// A failed publication leaves the receipt available for retry. Publication does
// not end the pending OAuth exchange or claim the credential commit.
func (s *oauthSessionStore) publishCallback(state, provider string, publish func() (string, error), expected ...*oauthCallbackReceipt) (string, error) {
	receipt, errReceipt := s.callbackForPending(state, provider)
	if errReceipt != nil {
		return "", errReceipt
	}
	// Provider normalization may depend on the selected registration's source.
	if len(expected) > 0 && receipt != expected[0] {
		return "", errOAuthSessionNotPending
	}
	receipt.mu.Lock()
	defer receipt.mu.Unlock()
	if receipt.closed.Load() || receipt.received {
		return "", errOAuthSessionNotPending
	}
	path, errPublish := publish()
	if errPublish != nil {
		return "", errPublish
	}
	receipt.path = path
	receipt.received = true
	if receipt.closed.Load() {
		return "", errOAuthSessionNotPending
	}
	return path, nil
}

// readOAuthCallbackFileForPendingSession consumes only a callback published for
// this registration. An old or unscoped file cannot authorize a new generation.
func readOAuthCallbackFileForPendingSession(path, provider, state string) ([]byte, error) {
	return oauthSessions.consumeCallback(path, provider, state)
}

func matchesOAuthCallbackGeneration(session oauthSession, expected []*oauthCallbackReceipt) bool {
	return len(expected) == 0 || session.callback == expected[0]
}

func (s *oauthSessionStore) consumeCallback(path, provider, state string) ([]byte, error) {
	receipt, errReceipt := s.callbackForPending(state, provider)
	if errReceipt != nil {
		return nil, errReceipt
	}
	return receipt.consume(path)
}

func (receipt *oauthCallbackReceipt) consume(path string) ([]byte, error) {
	receipt.mu.Lock()
	defer receipt.mu.Unlock()
	if receipt.closed.Load() || receipt.consumed {
		return nil, errOAuthSessionNotPending
	}
	if !receipt.received {
		return nil, os.ErrNotExist
	}
	requestedPath, errRequested := filepath.Abs(path)
	ownedPath, errOwned := filepath.Abs(receipt.path)
	if errRequested != nil || errOwned != nil || requestedPath != ownedPath {
		return nil, os.ErrNotExist
	}
	data, errRead := os.ReadFile(ownedPath)
	if errRead != nil {
		return nil, errRead
	}
	if errRemove := os.Remove(ownedPath); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
		return nil, errRemove
	}
	receipt.path = ""
	receipt.consumed = true
	if receipt.closed.Load() {
		return nil, errOAuthSessionNotPending
	}
	return data, nil
}
