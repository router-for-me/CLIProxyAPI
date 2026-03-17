package usage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

const persistHookDebounceDelay = 3 * time.Second

type persistencePayload struct {
	Version int                `json:"version"`
	SavedAt time.Time          `json:"saved_at"`
	Usage   StatisticsSnapshot `json:"usage"`
}

// Persistor periodically saves usage statistics to disk.
type Persistor struct {
	stats        *RequestStatistics
	path         string
	interval     time.Duration
	redactSource bool

	saveMu sync.Mutex

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}

	debounceMu    sync.Mutex
	debounceTimer *time.Timer

	started atomic.Bool
}

// Path returns the persistence file path.
func (p *Persistor) Path() string {
	if p == nil {
		return ""
	}
	return p.path
}

// Interval returns the persistence interval.
func (p *Persistor) Interval() time.Duration {
	if p == nil {
		return 0
	}
	return p.interval
}

// RedactSource indicates whether source fields are redacted on save.
func (p *Persistor) RedactSource() bool {
	if p == nil {
		return false
	}
	return p.redactSource
}

// NewPersistor constructs a Persistor from configuration and stats.
func NewPersistor(cfg *config.Config, stats *RequestStatistics) (*Persistor, error) {
	if cfg == nil || stats == nil {
		return nil, nil
	}
	if !cfg.UsagePersistence.Enabled {
		return nil, nil
	}
	path, err := ResolveUsagePersistencePath(cfg)
	if err != nil {
		return nil, err
	}
	intervalSeconds := cfg.UsagePersistence.IntervalSeconds
	if intervalSeconds <= 0 {
		intervalSeconds = config.DefaultUsagePersistenceIntervalSeconds
	}
	p := &Persistor{
		stats:        stats,
		path:         path,
		interval:     time.Duration(intervalSeconds) * time.Second,
		redactSource: cfg.UsagePersistence.RedactSource,
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}
	return p, nil
}

// ResolveUsagePersistencePath derives the usage persistence file path.
func ResolveUsagePersistencePath(cfg *config.Config) (string, error) {
	if cfg == nil {
		return "", errors.New("usage persistence: nil config")
	}
	path := strings.TrimSpace(cfg.UsagePersistence.Path)
	if path == "" {
		authDir := strings.TrimSpace(cfg.AuthDir)
		if authDir == "" {
			return "", errors.New("usage persistence: auth-dir is empty")
		}
		resolved, err := util.ResolveAuthDir(authDir)
		if err != nil {
			return "", err
		}
		if resolved == "" {
			return "", errors.New("usage persistence: auth-dir is empty")
		}
		path = filepath.Join(resolved, "usage", "usage.json")
	}
	if !filepath.IsAbs(path) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}
	return path, nil
}

// Load reads persisted usage statistics from disk.
func (p *Persistor) Load() (StatisticsSnapshot, bool) {
	if p == nil {
		return StatisticsSnapshot{}, false
	}
	snapshot, err := p.loadFromPath(p.path)
	if err == nil {
		return snapshot, true
	}
	log.Warnf("usage persistence: failed to load %s: %v", p.path, err)

	backupPath := p.path + ".bak"
	snapshot, err = p.loadFromPath(backupPath)
	if err == nil {
		return snapshot, true
	}
	log.Errorf("usage persistence: failed to load backup %s: %v", backupPath, err)
	return StatisticsSnapshot{}, false
}

// Start begins periodic persistence.
func (p *Persistor) Start() {
	if p == nil || p.interval <= 0 {
		return
	}
	if !p.started.CompareAndSwap(false, true) {
		return
	}
	p.persistHook(true)
	ticker := time.NewTicker(p.interval)
	go func() {
		defer close(p.doneCh)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := p.Save(); err != nil {
					log.Warnf("usage persistence: failed to save snapshot: %v", err)
				}
			case <-p.stopCh:
				return
			}
		}
	}()
}

// StopAndFlush stops persistence and performs a final save.
func (p *Persistor) StopAndFlush() {
	if p == nil {
		return
	}
	p.stopOnce.Do(func() {
		p.persistHook(false)
		if p.started.Load() {
			close(p.stopCh)
			if p.doneCh != nil {
				<-p.doneCh
			}
		}
		p.stopDebounce()
		if err := p.Save(); err != nil {
			log.Warnf("usage persistence: failed to save final snapshot: %v", err)
		}
	})
}

// TriggerSaveDebounced schedules a save with debounce.
func (p *Persistor) TriggerSaveDebounced() {
	if p == nil {
		return
	}
	p.debounceMu.Lock()
	if p.debounceTimer != nil {
		p.debounceTimer.Stop()
	}
	p.debounceTimer = time.AfterFunc(persistHookDebounceDelay, func() {
		if err := p.Save(); err != nil {
			log.Warnf("usage persistence: failed to save debounced snapshot: %v", err)
		}
		p.debounceMu.Lock()
		p.debounceTimer = nil
		p.debounceMu.Unlock()
	})
	p.debounceMu.Unlock()
}

func (p *Persistor) stopDebounce() {
	p.debounceMu.Lock()
	if p.debounceTimer != nil {
		p.debounceTimer.Stop()
		p.debounceTimer = nil
	}
	p.debounceMu.Unlock()
}

// Save writes the current snapshot to disk using a tmp -> backup -> rename flow.
func (p *Persistor) Save() error {
	if p == nil || p.stats == nil {
		return nil
	}
	p.saveMu.Lock()
	defer p.saveMu.Unlock()

	snapshot := p.stats.Snapshot()
	if p.redactSource {
		redactSnapshotSource(&snapshot)
	}
	payload := persistencePayload{
		Version: 1,
		SavedAt: time.Now().UTC(),
		Usage:   snapshot,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	dir := filepath.Dir(p.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmpPath := filepath.Join(dir, "."+filepath.Base(p.path)+".tmp")
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}

	backupPath := p.path + ".bak"
	_ = os.Remove(backupPath)
	if _, err := os.Stat(p.path); err == nil {
		if err := os.Rename(p.path, backupPath); err != nil {
			return err
		}
	}

	if err := os.Rename(tmpPath, p.path); err != nil {
		return err
	}
	return nil
}

func (p *Persistor) loadFromPath(path string) (StatisticsSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return StatisticsSnapshot{}, err
	}
	return decodeSnapshot(data)
}

func decodeSnapshot(data []byte) (StatisticsSnapshot, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return StatisticsSnapshot{}, err
	}
	if raw, ok := envelope["usage"]; ok && len(raw) > 0 {
		var snapshot StatisticsSnapshot
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			return StatisticsSnapshot{}, err
		}
		return snapshot, nil
	}
	var snapshot StatisticsSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return StatisticsSnapshot{}, err
	}
	return snapshot, nil
}

func (p *Persistor) persistHook(enable bool) {
	if enable {
		SetPersistHook(p.TriggerSaveDebounced)
		return
	}
	SetPersistHook(nil)
}

func redactSnapshotSource(snapshot *StatisticsSnapshot) {
	if snapshot == nil {
		return
	}
	for apiName, api := range snapshot.APIs {
		for modelName, model := range api.Models {
			for i := range model.Details {
				model.Details[i].Source = ""
				model.Details[i].AuthIndex = ""
			}
			api.Models[modelName] = model
		}
		snapshot.APIs[apiName] = api
	}
}
