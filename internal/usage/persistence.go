package usage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const defaultPersistDebounce = 2 * time.Second

type persistedSnapshot struct {
	Version    int                `json:"version"`
	SavedAt    time.Time          `json:"saved_at"`
	Statistics StatisticsSnapshot `json:"statistics"`
}

// Persistence keeps usage statistics on disk and restores them on startup.
type Persistence struct {
	path  string
	stats *RequestStatistics

	mu      sync.Mutex
	flushMu sync.Mutex
	timer   *time.Timer
	stopped bool
}

// StartPersistence loads any existing snapshot from disk and wires auto-save hooks.
func StartPersistence(stats *RequestStatistics, path string) (*Persistence, error) {
	if stats == nil {
		return nil, errors.New("usage persistence: nil statistics store")
	}
	path = filepath.Clean(path)
	p := &Persistence{
		path:  path,
		stats: stats,
	}
	if err := p.load(); err != nil {
		return nil, err
	}
	stats.SetChangeHook(p.scheduleSave)
	return p, nil
}

func (p *Persistence) load() error {
	data, err := os.ReadFile(p.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var snapshot persistedSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return err
	}
	result := p.stats.MergeSnapshot(snapshot.Statistics)
	if result.Added > 0 || result.Skipped > 0 {
		log.Infof("usage persistence restored statistics from %s (added=%d skipped=%d)", p.path, result.Added, result.Skipped)
	}
	return nil
}

func (p *Persistence) scheduleSave() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	if p.timer != nil {
		p.timer.Reset(defaultPersistDebounce)
		return
	}
	p.timer = time.AfterFunc(defaultPersistDebounce, func() {
		if err := p.Flush(); err != nil {
			log.Errorf("usage persistence flush failed: %v", err)
		}
	})
}

// Flush writes the current statistics snapshot to disk atomically.
func (p *Persistence) Flush() error {
	if p == nil || p.stats == nil {
		return nil
	}
	p.flushMu.Lock()
	defer p.flushMu.Unlock()

	p.mu.Lock()
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	p.mu.Unlock()

	snapshot := persistedSnapshot{
		Version:    1,
		SavedAt:    time.Now().UTC(),
		Statistics: p.stats.Snapshot(),
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0o755); err != nil {
		return err
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(p.path), filepath.Base(p.path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, p.path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// Stop disables future auto-saves and flushes pending data immediately.
func (p *Persistence) Stop() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	p.stopped = true
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	p.mu.Unlock()
	return p.Flush()
}
