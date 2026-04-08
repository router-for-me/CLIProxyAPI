package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// MetricsPersister handles saving and loading request statistics to/from a JSON file.
type MetricsPersister struct {
	stats      *RequestStatistics
	filePath   string
	mu         sync.Mutex
	stopCh     chan struct{}
	saveTicker *time.Ticker
}

// NewMetricsPersister creates a new MetricsPersister that stores data at the given path.
func NewMetricsPersister(stats *RequestStatistics, filePath string) *MetricsPersister {
	return &MetricsPersister{
		stats:    stats,
		filePath: filePath,
	}
}

// SaveToFile writes the current statistics snapshot to disk using atomic writes.
func (p *MetricsPersister) SaveToFile() error {
	if p == nil || p.stats == nil {
		return nil
	}

	snapshot := p.stats.Snapshot()
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(p.filePath)
	tmp, err := os.CreateTemp(dir, "metrics-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()

	if writeErr != nil || closeErr != nil {
		_ = os.Remove(tmpName)
		if writeErr != nil {
			return writeErr
		}
		return closeErr
	}

	if err := os.Rename(tmpName, p.filePath); err != nil {
		_ = os.Remove(tmpName)
		return err
	}

	return nil
}

// LoadFromFile reads a previously saved statistics snapshot from disk.
func (p *MetricsPersister) LoadFromFile() error {
	if p == nil || p.stats == nil {
		return nil
	}

	data, err := os.ReadFile(p.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.WithField("path", p.filePath).Debug("no metrics file found, starting fresh")
			return nil
		}
		return err
	}

	var snapshot StatisticsSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		log.WithError(err).WithField("path", p.filePath).Warn("corrupt metrics file, skipping load")
		return err
	}

	p.stats.MergeSnapshot(snapshot)
	log.WithField("path", p.filePath).Info("loaded metrics from file")
	return nil
}

// Start loads existing metrics from disk and begins periodic auto-save.
func (p *MetricsPersister) Start(interval time.Duration) {
	if p == nil {
		return
	}

	if err := p.LoadFromFile(); err != nil {
		log.WithError(err).Warn("failed to load metrics on startup")
	}

	p.stopCh = make(chan struct{})
	p.saveTicker = time.NewTicker(interval)

	go p.runAutoSave()
}

// Stop halts the periodic auto-save and performs a final flush to disk.
func (p *MetricsPersister) Stop() {
	if p == nil {
		return
	}

	p.mu.Lock()
	if p.saveTicker != nil {
		p.saveTicker.Stop()
		p.saveTicker = nil
	}
	if p.stopCh != nil {
		close(p.stopCh)
		p.stopCh = nil
	}
	p.mu.Unlock()

	if err := p.SaveToFile(); err != nil {
		log.WithError(err).Error("failed to save metrics on shutdown")
	} else {
		log.Info("metrics saved on shutdown")
	}
}

func (p *MetricsPersister) runAutoSave() {
	defer func() {
		if r := recover(); r != nil {
			log.WithField("panic", r).Error("metrics auto-save panicked")
		}
	}()

	for {
		p.mu.Lock()
		ticker := p.saveTicker
		stopCh := p.stopCh
		p.mu.Unlock()

		if ticker == nil || stopCh == nil {
			return
		}

		select {
		case <-ticker.C:
			if err := p.SaveToFile(); err != nil {
				log.WithError(err).Warn("failed to auto-save metrics")
			}
		case <-stopCh:
			return
		}
	}
}
