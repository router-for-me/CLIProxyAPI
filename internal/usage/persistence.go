package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	persistenceFileName = "usage-statistics.json"
	persistenceVersion  = 1
	autosaveInterval    = 5 * time.Minute
)

var (
	renameFile = os.Rename
	removeFile = os.Remove
)

func backupPath(path string) string {
	return path + ".bak"
}

type persistedSnapshot struct {
	Version    int                `json:"version"`
	ExportedAt time.Time          `json:"exported_at"`
	Usage      StatisticsSnapshot `json:"usage"`
}

type PersistenceManager struct {
	stats      *RequestStatistics
	path       string
	mu         sync.Mutex
	cancel     context.CancelFunc
	done       chan struct{}
	lastSaved  StatisticsSnapshot
	hasSaved   bool
}

func NewPersistenceManager(cfg *internalconfig.Config, configFilePath string, stats *RequestStatistics) *PersistenceManager {
	return &PersistenceManager{
		stats: stats,
		path:  resolvePersistencePath(cfg, configFilePath),
	}
}

func resolvePersistencePath(cfg *internalconfig.Config, configFilePath string) string {
	if cfg != nil {
		if explicit := strings.TrimSpace(cfg.UsageStatisticsPersistenceFile); explicit != "" {
			return filepath.Clean(explicit)
		}
	}

	if writable := util.WritablePath(); writable != "" {
		return filepath.Join(writable, "data", persistenceFileName)
	}

	configFilePath = strings.TrimSpace(configFilePath)
	if configFilePath == "" {
		return ""
	}

	base := filepath.Dir(configFilePath)
	if info, err := os.Stat(configFilePath); err == nil && info.IsDir() {
		base = configFilePath
	}
	if strings.TrimSpace(base) == "" {
		return ""
	}

	return filepath.Join(base, "data", persistenceFileName)
}

func EnabledForConfig(cfg *internalconfig.Config) bool {
	return cfg != nil && cfg.UsageStatisticsEnabled && cfg.UsageStatisticsPersistenceEnabled
}

func ResolvePersistencePathForTesting(cfg *internalconfig.Config, configFilePath string) string {
	return resolvePersistencePath(cfg, configFilePath)
}

func loadSnapshotFile(path string) (persistedSnapshot, bool, error) {
	var snapshot persistedSnapshot
	path = strings.TrimSpace(path)
	if path == "" {
		return snapshot, false, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return snapshot, false, nil
		}
		return snapshot, false, err
	}

	if err := json.Unmarshal(data, &snapshot); err != nil {
		return persistedSnapshot{}, false, fmt.Errorf("decode usage snapshot: %w", err)
	}
	if snapshot.Version != persistenceVersion {
		return persistedSnapshot{}, false, fmt.Errorf("unsupported usage snapshot version %d", snapshot.Version)
	}

	return snapshot, true, nil
}

func saveSnapshotFile(path string, snapshot StatisticsSnapshot) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	payload := persistedSnapshot{
		Version:    persistenceVersion,
		ExportedAt: time.Now().UTC(),
		Usage:      snapshot,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("encode usage snapshot: %w", err)
	}
	data = append(data, '\n')

	return atomicWriteJSON(path, data)
}

func snapshotsEqual(a, b StatisticsSnapshot) bool {
	encodedA, errA := json.Marshal(a)
	encodedB, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(encodedA) == string(encodedB)
}

func atomicWriteJSON(path string, data []byte) error {
	tmpFile, err := os.CreateTemp(filepath.Dir(path), "usage-*.json")
	if err != nil {
		return err
	}

	tmpName := tmpFile.Name()
	defer func() {
		_ = tmpFile.Close()
		_ = removeFile(tmpName)
	}()

	if _, err := tmpFile.Write(data); err != nil {
		return err
	}
	if err := tmpFile.Chmod(0o600); err != nil {
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}

	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return renameFile(tmpName, path)
		}
		return err
	}

	return replaceFileWithBackup(path, tmpName)
}

func replaceFileWithBackup(path, replacementPath string) error {
	backup := backupPath(path)
	if err := removeFile(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := renameFile(path, backup); err != nil {
		return err
	}
	if err := renameFile(replacementPath, path); err != nil {
		if restoreErr := renameFile(backup, path); restoreErr != nil {
			return fmt.Errorf("replace usage snapshot: %w (restore backup: %v)", err, restoreErr)
		}
		return fmt.Errorf("replace usage snapshot: %w", err)
	}
	if err := removeFile(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.WithError(err).Warnf("usage persistence backup cleanup failed: %s", backup)
	}
	return nil
}

func (m *PersistenceManager) Restore() error {
	if m == nil {
		return nil
	}

	primaryPath := strings.TrimSpace(m.path)
	snapshot, ok, err := loadSnapshotFile(primaryPath)
	if err != nil && primaryPath != "" {
		backup := backupPath(primaryPath)
		backupSnapshot, backupOK, backupErr := loadSnapshotFile(backup)
		if backupErr == nil && backupOK {
			snapshot = backupSnapshot
			ok = true
			err = nil
			primaryPath = backup
		}
		if err != nil {
			return err
		}
	}
	if !ok && primaryPath != "" {
		primaryPath = backupPath(primaryPath)
		snapshot, ok, err = loadSnapshotFile(primaryPath)
		if err != nil {
			return err
		}
	}
	if !ok {
		return nil
	}
	if m.stats == nil {
		log.Debug("usage persistence restore skipped: statistics store is nil")
		return nil
	}

	result := m.stats.MergeSnapshot(snapshot.Usage)
	m.mu.Lock()
	m.hasSaved = false
	m.mu.Unlock()
	log.Infof("usage persistence restored snapshot from %s (added=%d skipped=%d)", primaryPath, result.Added, result.Skipped)
	return nil
}

func (m *PersistenceManager) Save() error {
	if m == nil || m.stats == nil {
		return nil
	}
	snapshot := m.stats.Snapshot()
	m.mu.Lock()
	if m.hasSaved && snapshotsEqual(m.lastSaved, snapshot) {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	if err := saveSnapshotFile(m.path, snapshot); err != nil {
		return err
	}
	m.mu.Lock()
	m.lastSaved = snapshot
	m.hasSaved = true
	m.mu.Unlock()
	return nil
}

func (m *PersistenceManager) Start(ctx context.Context) {
	if m == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		return
	}

	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	m.cancel = cancel
	m.done = done

	go func() {
		defer close(done)
		ticker := time.NewTicker(autosaveInterval)
		defer ticker.Stop()

		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				if err := m.Save(); err != nil {
					log.WithError(err).Warn("usage persistence autosave failed")
				}
			}
		}
	}()
}

func (m *PersistenceManager) StopAndSave() error {
	if m == nil {
		return nil
	}

	m.mu.Lock()
	cancel := m.cancel
	done := m.done
	m.cancel = nil
	m.done = nil
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}

	return m.Save()
}
