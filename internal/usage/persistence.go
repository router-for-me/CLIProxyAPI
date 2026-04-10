package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	defaultPersistDebounce      = 2 * time.Second
	defaultShutdownFlushTimeout = 5 * time.Second
	defaultMaxRequestDetails    = 1000
	statsSnapshotVersion        = 1
)

type snapshotPayload struct {
	Version    int                `json:"version"`
	ExportedAt time.Time          `json:"exported_at"`
	Usage      StatisticsSnapshot `json:"usage"`
}

type SnapshotStore interface {
	Load(ctx context.Context) (StatisticsSnapshot, error)
	Save(ctx context.Context, snapshot StatisticsSnapshot) error
	Path() string
}

type FileSnapshotStore struct {
	path string
}

func NewFileSnapshotStore(path string) *FileSnapshotStore {
	return &FileSnapshotStore{path: path}
}

func (s *FileSnapshotStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *FileSnapshotStore) Load(ctx context.Context) (StatisticsSnapshot, error) {
	var empty StatisticsSnapshot
	if s == nil || s.path == "" {
		return empty, nil
	}
	if err := ctxErr(ctx); err != nil {
		return empty, err
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return empty, nil
		}
		return empty, err
	}
	if len(data) == 0 {
		return empty, nil
	}
	var payload snapshotPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return empty, err
	}
	if payload.Version != 0 && payload.Version != statsSnapshotVersion {
		return empty, fmt.Errorf("unsupported version %d", payload.Version)
	}
	return payload.Usage, nil
}

func (s *FileSnapshotStore) Save(ctx context.Context, snapshot StatisticsSnapshot) error {
	if s == nil || s.path == "" {
		return nil
	}
	if err := ctxErr(ctx); err != nil {
		return err
	}
	payload := snapshotPayload{
		Version:    statsSnapshotVersion,
		ExportedAt: time.Now().UTC(),
		Usage:      snapshot,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(s.path), ".usage-statistics-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	cleanup := true
	defer func() {
		_ = tmpFile.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmpFile.Write(data); err != nil {
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

type PersistenceManager struct {
	store    SnapshotStore
	stats    *RequestStatistics
	debounce time.Duration

	mu     sync.Mutex
	timer  *time.Timer
	closed bool
	dirty  bool
}

func NewPersistenceManager(store SnapshotStore, stats *RequestStatistics) *PersistenceManager {
	return &PersistenceManager{
		store:    store,
		stats:    stats,
		debounce: defaultPersistDebounce,
	}
}

func ResolvePersistencePath(configPath, value string) string {
	value = filepath.Clean(value)
	if value == "." || value == "" {
		value = "usage-statistics.json"
	}
	if filepath.IsAbs(value) {
		return value
	}
	base := filepath.Dir(configPath)
	if base == "." || base == "" {
		if abs, err := filepath.Abs(value); err == nil {
			return abs
		}
		return value
	}
	return filepath.Join(base, value)
}

func (m *PersistenceManager) Load(ctx context.Context) error {
	if m == nil || m.store == nil || m.stats == nil {
		return nil
	}
	snapshot, err := m.store.Load(ctx)
	if err != nil {
		return err
	}
	m.stats.RestoreSnapshot(snapshot)
	if snapshot.TotalRequests > 0 || snapshot.TotalTokens > 0 {
		log.Infof("usage statistics restored from %s: total_requests=%d total_tokens=%d", m.store.Path(), snapshot.TotalRequests, snapshot.TotalTokens)
	}
	return nil
}

func (m *PersistenceManager) MarkDirty() {
	if m == nil || m.store == nil || m.stats == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.dirty = true
	if m.timer == nil {
		m.timer = time.AfterFunc(m.debounce, m.flushAsync)
		return
	}
	m.timer.Reset(m.debounce)
}

func (m *PersistenceManager) Flush(ctx context.Context) error {
	if m == nil || m.store == nil || m.stats == nil {
		return nil
	}
	m.mu.Lock()
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
	if !m.dirty {
		m.mu.Unlock()
		return nil
	}
	m.dirty = false
	m.mu.Unlock()

	snapshot := m.stats.Snapshot()
	if err := m.store.Save(ctx, snapshot); err != nil {
		m.mu.Lock()
		if !m.closed {
			m.dirty = true
		}
		m.mu.Unlock()
		return err
	}
	return nil
}

func (m *PersistenceManager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	m.closed = true
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
	m.mu.Unlock()
	if ctx == nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultShutdownFlushTimeout)
		defer cancel()
		ctx = shutdownCtx
	}
	return m.Flush(ctx)
}

func (m *PersistenceManager) flushAsync() {
	ctx, cancel := context.WithTimeout(context.Background(), defaultShutdownFlushTimeout)
	defer cancel()
	if err := m.Flush(ctx); err != nil {
		log.WithError(err).Warn("failed to persist usage statistics snapshot")
	}
}

func ctxErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
