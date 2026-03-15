package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

type persistencePayload struct {
	Version int                `json:"version"`
	SavedAt time.Time          `json:"saved_at"`
	Usage   StatisticsSnapshot `json:"usage"`
}

type PersistenceStatus struct {
	Enabled         bool      `json:"enabled"`
	Path            string    `json:"path"`
	IntervalSeconds int       `json:"interval_seconds"`
	LastSavedAt     time.Time `json:"last_saved_at,omitempty"`
	LastLoadedAt    time.Time `json:"last_loaded_at,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
}

type PersistenceLoadResult struct {
	Added   int64 `json:"added"`
	Skipped int64 `json:"skipped"`
}

type PersistenceManager struct {
	mu sync.Mutex

	stats   *RequestStatistics
	baseDir string

	enabled  bool
	path     string
	interval time.Duration

	stopCh  chan struct{}
	running bool

	lastSavedAt  time.Time
	lastLoadedAt time.Time
	lastError    string
}

func NewPersistenceManager(stats *RequestStatistics, baseDir string) *PersistenceManager {
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		baseDir = "."
	}
	return &PersistenceManager{stats: stats, baseDir: baseDir}
}

func (m *PersistenceManager) ApplyConfig(cfg config.UsagePersistenceConfig) {
	if m == nil {
		return
	}

	enabled := cfg.Enabled
	path := m.resolvePath(cfg.FilePath)
	intervalSeconds := cfg.IntervalSeconds
	if intervalSeconds <= 0 {
		intervalSeconds = 30
	}
	interval := time.Duration(intervalSeconds) * time.Second
	maxDetailsPerModel := cfg.MaxDetailsPerModel
	if maxDetailsPerModel == 0 {
		maxDetailsPerModel = 300
	}
	if m.stats != nil {
		m.stats.SetMaxDetailsPerModel(maxDetailsPerModel)
	}

	m.mu.Lock()
	shouldRestart := m.running && (m.path != path || m.interval != interval)
	shouldStop := m.running && (!enabled || shouldRestart)
	if shouldStop {
		close(m.stopCh)
		m.running = false
		m.stopCh = nil
	}
	m.enabled = enabled
	m.path = path
	m.interval = interval
	needStart := m.enabled && !m.running
	if needStart {
		m.stopCh = make(chan struct{})
		m.running = true
	}
	m.mu.Unlock()

	if !enabled {
		return
	}

	if needStart || shouldRestart {
		_, _ = m.LoadNow()
		go m.run()
	}
}

func (m *PersistenceManager) resolvePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		trimmed = "usage-statistics.json"
	}
	if filepath.IsAbs(trimmed) {
		return trimmed
	}
	return filepath.Join(m.baseDir, trimmed)
}

func (m *PersistenceManager) run() {
	m.mu.Lock()
	stopCh := m.stopCh
	interval := m.interval
	m.mu.Unlock()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			_, _ = m.SaveNow()
		case <-stopCh:
			return
		}
	}
}

func (m *PersistenceManager) SaveNow() (PersistenceStatus, error) {
	if m == nil || m.stats == nil {
		return PersistenceStatus{}, fmt.Errorf("usage persistence unavailable")
	}

	m.mu.Lock()
	path := m.path
	if path == "" {
		path = m.resolvePath("usage-statistics.json")
		m.path = path
	}
	m.mu.Unlock()

	snapshot := m.stats.Snapshot()
	payload := persistencePayload{Version: 2, SavedAt: time.Now().UTC(), Usage: snapshot}
	data, err := json.Marshal(payload)
	if err != nil {
		m.recordError(err)
		return m.Status(), err
	}

	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		m.recordError(err)
		return m.Status(), err
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(path), "usage-persist-*.json")
	if err != nil {
		m.recordError(err)
		return m.Status(), err
	}
	tmpName := tmpFile.Name()
	writeErr := func(inner error) error {
		_ = tmpFile.Close()
		_ = os.Remove(tmpName)
		m.recordError(inner)
		return inner
	}

	if _, err = tmpFile.Write(data); err != nil {
		return m.Status(), writeErr(err)
	}
	if err = tmpFile.Sync(); err != nil {
		return m.Status(), writeErr(err)
	}
	if err = tmpFile.Close(); err != nil {
		return m.Status(), writeErr(err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		m.recordError(err)
		return m.Status(), err
	}

	m.mu.Lock()
	m.lastSavedAt = payload.SavedAt
	m.lastError = ""
	m.mu.Unlock()
	return m.Status(), nil
}

func (m *PersistenceManager) LoadNow() (PersistenceLoadResult, error) {
	if m == nil || m.stats == nil {
		return PersistenceLoadResult{}, fmt.Errorf("usage persistence unavailable")
	}

	m.mu.Lock()
	path := m.path
	if path == "" {
		path = m.resolvePath("usage-statistics.json")
		m.path = path
	}
	m.mu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			m.mu.Lock()
			m.lastError = ""
			m.mu.Unlock()
			return PersistenceLoadResult{}, nil
		}
		m.recordError(err)
		return PersistenceLoadResult{}, err
	}

	var payload persistencePayload
	if err = json.Unmarshal(data, &payload); err != nil {
		m.recordError(err)
		return PersistenceLoadResult{}, err
	}
	// Accept legacy payloads without explicit version (treated as v0) and current v1/v2 payloads.
	// v0 compatibility keeps previously exported snapshots loadable after upgrades.
	if payload.Version != 0 && payload.Version != 1 && payload.Version != 2 {
		err = fmt.Errorf("unsupported usage persistence version: %d", payload.Version)
		m.recordError(err)
		return PersistenceLoadResult{}, err
	}

	mergeResult := m.stats.MergeSnapshot(payload.Usage)
	loadedAt := time.Now().UTC()
	m.mu.Lock()
	m.lastLoadedAt = loadedAt
	m.lastError = ""
	m.mu.Unlock()

	return PersistenceLoadResult{Added: mergeResult.Added, Skipped: mergeResult.Skipped}, nil
}

func (m *PersistenceManager) Status() PersistenceStatus {
	if m == nil {
		return PersistenceStatus{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	interval := 0
	if m.interval > 0 {
		interval = int(m.interval / time.Second)
	}
	return PersistenceStatus{
		Enabled:         m.enabled,
		Path:            m.path,
		IntervalSeconds: interval,
		LastSavedAt:     m.lastSavedAt,
		LastLoadedAt:    m.lastLoadedAt,
		LastError:       m.lastError,
	}
}

func (m *PersistenceManager) Stop(flush bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	enabled := m.enabled
	if m.running && m.stopCh != nil {
		close(m.stopCh)
	}
	m.running = false
	m.stopCh = nil
	m.mu.Unlock()

	if flush && enabled {
		_, _ = m.SaveNow()
	}
}

func (m *PersistenceManager) recordError(err error) {
	if m == nil {
		return
	}
	m.mu.Lock()
	if err == nil {
		m.lastError = ""
	} else {
		m.lastError = err.Error()
	}
	m.mu.Unlock()
}
