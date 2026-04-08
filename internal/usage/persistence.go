package usage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const defaultPersistenceFlushInterval = 200 * time.Millisecond

type statisticsPersistence struct {
	path          string
	flushInterval time.Duration
	triggerCh     chan struct{}
	flushCh       chan chan error
	stopCh        chan struct{}
	stopOnce      sync.Once
}

func newStatisticsPersistence(path string) *statisticsPersistence {
	return &statisticsPersistence{
		path:          path,
		flushInterval: defaultPersistenceFlushInterval,
		triggerCh:     make(chan struct{}, 1),
		flushCh:       make(chan chan error),
		stopCh:        make(chan struct{}),
	}
}

// SaveSnapshotToFile writes a statistics snapshot to disk using an atomic replace.
func SaveSnapshotToFile(path string, snapshot StatisticsSnapshot) error {
	trimmedPath := strings.TrimSpace(path)
	if trimmedPath == "" {
		return fmt.Errorf("usage statistics persistence path is empty")
	}

	dir := filepath.Dir(trimmedPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create usage statistics directory: %w", err)
	}

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal usage statistics snapshot: %w", err)
	}
	data = append(data, '\n')

	tmpFile, err := os.CreateTemp(dir, "usage-statistics-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary usage statistics file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write temporary usage statistics file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temporary usage statistics file: %w", err)
	}

	if err := os.Rename(tmpPath, trimmedPath); err != nil {
		if removeErr := os.Remove(trimmedPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("replace usage statistics file: %w", err)
		}
		if retryErr := os.Rename(tmpPath, trimmedPath); retryErr != nil {
			return fmt.Errorf("replace usage statistics file: %w", retryErr)
		}
	}

	return nil
}

// LoadSnapshotFromFile loads a statistics snapshot from disk.
func LoadSnapshotFromFile(path string) (StatisticsSnapshot, error) {
	trimmedPath := strings.TrimSpace(path)
	if trimmedPath == "" {
		return StatisticsSnapshot{}, fmt.Errorf("usage statistics persistence path is empty")
	}

	data, err := os.ReadFile(trimmedPath)
	if err != nil {
		return StatisticsSnapshot{}, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return StatisticsSnapshot{}, nil
	}

	var snapshot StatisticsSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return StatisticsSnapshot{}, fmt.Errorf("unmarshal usage statistics snapshot: %w", err)
	}

	return snapshot, nil
}

// EnablePersistence configures automatic statistics persistence and restores any existing snapshot.
func (s *RequestStatistics) EnablePersistence(path string) error {
	if s == nil {
		return nil
	}

	trimmedPath := strings.TrimSpace(path)
	if trimmedPath == "" {
		return fmt.Errorf("usage statistics persistence path is empty")
	}
	if !filepath.IsAbs(trimmedPath) {
		absPath, err := filepath.Abs(trimmedPath)
		if err != nil {
			return fmt.Errorf("resolve usage statistics persistence path: %w", err)
		}
		trimmedPath = absPath
	}

	persistence := newStatisticsPersistence(trimmedPath)
	go persistence.run(s)

	s.mu.Lock()
	previousPersistence := s.persistence
	s.persistence = persistence
	s.mu.Unlock()

	if previousPersistence != nil {
		previousPersistence.stop()
	}

	snapshot, err := LoadSnapshotFromFile(trimmedPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	s.MergeSnapshot(snapshot)
	return nil
}

// FlushPersistence writes the current statistics snapshot to disk immediately.
func (s *RequestStatistics) FlushPersistence() error {
	if s == nil {
		return nil
	}

	s.mu.RLock()
	persistence := s.persistence
	s.mu.RUnlock()
	if persistence == nil {
		return nil
	}

	return persistence.flushNow(s.Snapshot())
}

// DisablePersistence detaches local statistics persistence immediately.
func (s *RequestStatistics) DisablePersistence() {
	if s == nil {
		return
	}

	s.mu.Lock()
	persistence := s.persistence
	s.persistence = nil
	s.mu.Unlock()

	if persistence != nil {
		persistence.stop()
	}
}

func (s *RequestStatistics) schedulePersistenceSave(persistence *statisticsPersistence) {
	if s == nil || persistence == nil {
		return
	}
	persistence.schedule()
}

func (p *statisticsPersistence) run(stats *RequestStatistics) {
	var (
		timer  *time.Timer
		timerC <-chan time.Time
	)

	for {
		select {
		case <-p.triggerCh:
			if timer == nil {
				timer = time.NewTimer(p.flushInterval)
				timerC = timer.C
				continue
			}
			if !timer.Stop() {
				select {
				case <-timerC:
				default:
				}
			}
			timer.Reset(p.flushInterval)
		case done := <-p.flushCh:
			if timer != nil {
				if !timer.Stop() {
					select {
					case <-timerC:
					default:
					}
				}
				timer = nil
				timerC = nil
			}
			done <- SaveSnapshotToFile(p.path, stats.Snapshot())
		case <-timerC:
			if err := SaveSnapshotToFile(p.path, stats.Snapshot()); err != nil {
				log.WithError(err).Warn("failed to persist usage statistics snapshot")
			}
			timer = nil
			timerC = nil
		case <-p.stopCh:
			if timer != nil {
				if !timer.Stop() {
					select {
					case <-timerC:
					default:
					}
				}
			}
			return
		}
	}
}

func (p *statisticsPersistence) schedule() {
	select {
	case p.triggerCh <- struct{}{}:
	default:
	}
}

func (p *statisticsPersistence) flushNow(snapshot StatisticsSnapshot) error {
	done := make(chan error, 1)
	select {
	case p.flushCh <- done:
		return <-done
	case <-p.stopCh:
		return SaveSnapshotToFile(p.path, snapshot)
	}
}

func (p *statisticsPersistence) stop() {
	p.stopOnce.Do(func() {
		close(p.stopCh)
	})
}
