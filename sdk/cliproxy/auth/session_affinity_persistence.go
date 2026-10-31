package auth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	log "github.com/sirupsen/logrus"
)

const sessionAffinityPersistenceSchema = "cliproxy-session-affinity@v1"
const sessionAffinityPersistenceInterval = 5 * time.Second

type sessionAffinitySnapshot struct {
	Schema       string                                      `json:"schema"`
	SavedAt      time.Time                                   `json:"saved_at"`
	CacheEntries []SessionCacheSnapshotEntry                 `json:"cache_entries"`
	LCPGroups    []cliproxysession.MerklePrefixSnapshotEntry `json:"lcp_groups"`
}

// SessionAffinityPersistenceStatus reports the current durable snapshot state.
type SessionAffinityPersistenceStatus struct {
	Enabled      bool      `json:"enabled"`
	Path         string    `json:"path,omitempty"`
	SavedAt      time.Time `json:"saved_at,omitempty"`
	CacheEntries int       `json:"cache_entries"`
	LCPGroups    int       `json:"lcp_groups"`
	Error        string    `json:"error,omitempty"`
}

type sessionAffinityPersistence struct {
	path     string
	mu       sync.Mutex
	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once
	status   SessionAffinityPersistenceStatus
}

func (s *SessionAffinitySelector) startPersistence(path string) {
	path = filepath.Clean(path)
	if path == "." || path == "" {
		return
	}
	p := &sessionAffinityPersistence{
		path:   path,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
		status: SessionAffinityPersistenceStatus{Enabled: true, Path: path},
	}
	s.persistence = p
	if status := s.restorePersistence(); status.Error != "" {
		log.WithError(errors.New(status.Error)).Warn("session affinity snapshot restore failed")
	}
	go func() {
		defer close(p.doneCh)
		ticker := time.NewTicker(sessionAffinityPersistenceInterval)
		defer ticker.Stop()
		lastError := ""
		for {
			select {
			case <-ticker.C:
				lastError = reportSessionAffinityFlush(s.FlushPersistence(), lastError)
			case <-p.stopCh:
				return
			}
		}
	}()
}

// reportSessionAffinityFlush logs a periodic flush failure once per distinct
// error and logs when writes recover, so a persistent failure (permissions, full
// disk, read-only mount) is visible without a warning every interval. It returns
// the error to compare against on the next flush.
func reportSessionAffinityFlush(status SessionAffinityPersistenceStatus, lastError string) string {
	switch {
	case status.Error != "" && status.Error != lastError:
		log.WithError(errors.New(status.Error)).Warn("session affinity snapshot flush failed")
	case status.Error == "" && lastError != "":
		log.Info("session affinity snapshot flush recovered")
	}
	return status.Error
}

func (s *SessionAffinitySelector) restorePersistence() SessionAffinityPersistenceStatus {
	if s == nil || s.persistence == nil {
		return SessionAffinityPersistenceStatus{}
	}
	p := s.persistence
	p.mu.Lock()
	defer p.mu.Unlock()
	status := SessionAffinityPersistenceStatus{Enabled: true, Path: p.path}
	raw, err := os.ReadFile(p.path)
	if errors.Is(err, os.ErrNotExist) {
		p.status = status
		return status
	}
	if err != nil {
		status.Error = err.Error()
		p.status = status
		return status
	}
	var snapshot sessionAffinitySnapshot
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		status.Error = err.Error()
		p.status = status
		return status
	}
	if snapshot.Schema != sessionAffinityPersistenceSchema {
		status.Error = "unsupported snapshot schema"
		p.status = status
		return status
	}
	if s.cache != nil {
		status.CacheEntries = s.cache.Restore(snapshot.CacheEntries)
	}
	if s.matcher != nil {
		status.LCPGroups = s.matcher.Restore(snapshot.LCPGroups)
	}
	status.SavedAt = snapshot.SavedAt
	p.status = status
	return status
}

// FlushPersistence atomically writes the current session and LCP bindings.
func (s *SessionAffinitySelector) FlushPersistence() SessionAffinityPersistenceStatus {
	if s == nil || s.persistence == nil {
		return SessionAffinityPersistenceStatus{}
	}
	p := s.persistence
	p.mu.Lock()
	defer p.mu.Unlock()
	snapshot := sessionAffinitySnapshot{Schema: sessionAffinityPersistenceSchema, SavedAt: time.Now().UTC()}
	if s.cache != nil {
		snapshot.CacheEntries = s.cache.Snapshot()
	}
	if s.matcher != nil {
		snapshot.LCPGroups = s.matcher.Snapshot()
	}
	status := SessionAffinityPersistenceStatus{
		Enabled:      true,
		Path:         p.path,
		SavedAt:      snapshot.SavedAt,
		CacheEntries: len(snapshot.CacheEntries),
		LCPGroups:    len(snapshot.LCPGroups),
	}
	if err := writeSessionAffinitySnapshot(p.path, snapshot); err != nil {
		status.Error = err.Error()
	}
	p.status = status
	return status
}

// PersistenceStatus returns the last restore or flush result.
func (s *SessionAffinitySelector) PersistenceStatus() SessionAffinityPersistenceStatus {
	if s == nil || s.persistence == nil {
		return SessionAffinityPersistenceStatus{}
	}
	s.persistence.mu.Lock()
	defer s.persistence.mu.Unlock()
	return s.persistence.status
}

func (s *SessionAffinitySelector) stopPersistence() {
	if s == nil || s.persistence == nil {
		return
	}
	p := s.persistence
	// Stop may run more than once (or concurrently). Only the first call closes the
	// ticker and writes the final snapshot; a later flush would run after the
	// selector cleared its state and overwrite the snapshot with an empty one.
	p.stopOnce.Do(func() {
		close(p.stopCh)
		<-p.doneCh
		status := s.FlushPersistence()
		if status.Error != "" {
			log.WithError(errors.New(status.Error)).Warn("session affinity snapshot flush failed")
		}
	})
}

func writeSessionAffinitySnapshot(path string, snapshot sessionAffinitySnapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".session-affinity-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(append(raw, '\n'))
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
