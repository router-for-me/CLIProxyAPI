package usage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	defaultPersistDebounce     = 2 * time.Second
	defaultDetailRetentionDays = 14
	usageSummaryFileName       = "usage-summary.json"
	usageDailyDirectoryName    = "usage-days"
	legacyUsageFileName        = "usage-statistics.json"
)

type persistedSummary struct {
	Version    int                `json:"version"`
	SavedAt    time.Time          `json:"saved_at"`
	Statistics StatisticsSnapshot `json:"statistics"`
}

type persistedDayDetails struct {
	Version    int                `json:"version"`
	Day        string             `json:"day"`
	SavedAt    time.Time          `json:"saved_at"`
	Statistics StatisticsSnapshot `json:"statistics"`
}

type legacyPersistedSnapshot struct {
	Version    int                `json:"version"`
	SavedAt    time.Time          `json:"saved_at"`
	ExportedAt time.Time          `json:"exported_at"`
	Usage      StatisticsSnapshot `json:"usage"`
	Statistics StatisticsSnapshot `json:"statistics"`
}

// Persistence keeps usage statistics on disk and restores them on startup.
type Persistence struct {
	baseDir       string
	summaryPath   string
	dailyDir      string
	retentionDays int
	stats         *RequestStatistics

	mu        sync.Mutex
	flushMu   sync.Mutex
	timer     *time.Timer
	stopped   bool
	dirtyDays map[string]struct{}
}

// StartPersistence loads persisted usage data and wires auto-save hooks.
func StartPersistence(stats *RequestStatistics, baseDir string, retentionDays int) (*Persistence, error) {
	if stats == nil {
		return nil, errors.New("usage persistence: nil statistics store")
	}
	if retentionDays <= 0 {
		retentionDays = defaultDetailRetentionDays
	}
	baseDir = filepath.Clean(baseDir)
	p := &Persistence{
		baseDir:       baseDir,
		summaryPath:   filepath.Join(baseDir, usageSummaryFileName),
		dailyDir:      filepath.Join(baseDir, usageDailyDirectoryName),
		retentionDays: retentionDays,
		stats:         stats,
		dirtyDays:     make(map[string]struct{}),
	}
	if err := p.load(); err != nil {
		return nil, err
	}
	stats.SetChangeHook(p.scheduleSave)
	return p, nil
}

func (p *Persistence) load() error {
	if err := p.loadSummary(); err != nil {
		return err
	}
	if err := p.loadRecentDayFiles(); err != nil {
		return err
	}
	p.stats.PruneDetailsBefore(p.detailCutoff(time.Now().UTC()))
	return nil
}

func (p *Persistence) loadSummary() error {
	data, err := os.ReadFile(p.summaryPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		legacyPath := filepath.Join(p.baseDir, legacyUsageFileName)
		data, err = os.ReadFile(legacyPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
	}
	snapshot, err := decodeSummaryPayload(data)
	if err != nil {
		return err
	}
	p.stats.ReplaceSummarySnapshot(snapshot)
	return nil
}

func (p *Persistence) loadRecentDayFiles() error {
	entries, err := os.ReadDir(p.dailyDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	cutoffDay := p.detailCutoff(time.Now().UTC()).Format("2006-01-02")
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		day, ok := dailyFileDay(entry.Name())
		if !ok || day < cutoffDay {
			continue
		}
		data, err := os.ReadFile(filepath.Join(p.dailyDir, entry.Name()))
		if err != nil {
			return err
		}
		var payload persistedDayDetails
		if err := json.Unmarshal(data, &payload); err != nil {
			return err
		}
		p.stats.AttachDetailsSnapshot(payload.Statistics)
	}
	return nil
}

func (p *Persistence) scheduleSave() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	for day := range p.stats.ConsumeChangedDays() {
		p.dirtyDays[day] = struct{}{}
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

// Flush writes the current summary and recent day detail snapshots to disk atomically.
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

	cutoff := p.detailCutoff(time.Now().UTC())
	p.stats.PruneDetailsBefore(cutoff)
	snapshot := p.stats.Snapshot()
	summary := p.stats.SummarySnapshot()
	daySnapshots := buildDaySnapshots(snapshot, cutoff)
	dirtyDays := p.takeDirtyDays(daySnapshots)

	if err := os.MkdirAll(p.baseDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(p.dailyDir, 0o755); err != nil {
		return err
	}
	if err := writeJSONAtomically(p.summaryPath, persistedSummary{
		Version:    2,
		SavedAt:    time.Now().UTC(),
		Statistics: summary,
	}); err != nil {
		return err
	}

	for day := range dirtyDays {
		daySnapshot, ok := daySnapshots[day]
		if !ok {
			if err := os.Remove(filepath.Join(p.dailyDir, day+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		path := filepath.Join(p.dailyDir, day+".json")
		if err := writeJSONAtomically(path, persistedDayDetails{
			Version:    2,
			Day:        day,
			SavedAt:    time.Now().UTC(),
			Statistics: daySnapshot,
		}); err != nil {
			return err
		}
	}

	return p.cleanupExpiredDailyFiles(cutoff)
}

func (p *Persistence) cleanupExpiredDailyFiles(cutoff time.Time) error {
	entries, err := os.ReadDir(p.dailyDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	cutoffDay := cutoff.Format("2006-01-02")
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		day, ok := dailyFileDay(name)
		if !ok {
			continue
		}
		if day >= cutoffDay {
			continue
		}
		if err := os.Remove(filepath.Join(p.dailyDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
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

func (p *Persistence) detailCutoff(now time.Time) time.Time {
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return dayStart.AddDate(0, 0, -(p.retentionDays - 1))
}

func (p *Persistence) takeDirtyDays(daySnapshots map[string]StatisticsSnapshot) map[string]struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make(map[string]struct{}, len(p.dirtyDays))
	for day := range p.dirtyDays {
		result[day] = struct{}{}
	}
	p.dirtyDays = make(map[string]struct{})
	for day := range daySnapshots {
		path := filepath.Join(p.dailyDir, day+".json")
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			result[day] = struct{}{}
		}
	}
	return result
}

func buildDaySnapshots(snapshot StatisticsSnapshot, cutoff time.Time) map[string]StatisticsSnapshot {
	result := make(map[string]StatisticsSnapshot)
	for apiName, apiSnapshot := range snapshot.APIs {
		for modelName, modelSnapshot := range apiSnapshot.Models {
			for _, detail := range modelSnapshot.Details {
				if detail.Timestamp.Before(cutoff) {
					continue
				}
				day := detail.Timestamp.UTC().Format("2006-01-02")
				daySnapshot := result[day]
				if daySnapshot.APIs == nil {
					daySnapshot.APIs = make(map[string]APISnapshot)
				}
				apiDaySnapshot := daySnapshot.APIs[apiName]
				if apiDaySnapshot.Models == nil {
					apiDaySnapshot.Models = make(map[string]ModelSnapshot)
				}
				modelDaySnapshot := apiDaySnapshot.Models[modelName]
				modelDaySnapshot.Details = append(modelDaySnapshot.Details, detail)
				apiDaySnapshot.Models[modelName] = modelDaySnapshot
				daySnapshot.APIs[apiName] = apiDaySnapshot
				result[day] = daySnapshot
			}
		}
	}
	return result
}

func writeJSONAtomically(path string, payload any) error {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	tmpFile, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
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
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func dailyFileDay(name string) (string, bool) {
	if !strings.HasSuffix(name, ".json") {
		return "", false
	}
	day := strings.TrimSuffix(name, ".json")
	if len(day) != len("2006-01-02") {
		return "", false
	}
	if _, err := time.Parse("2006-01-02", day); err != nil {
		return "", false
	}
	return day, true
}

func decodeSummaryPayload(data []byte) (StatisticsSnapshot, error) {
	var current persistedSummary
	if err := json.Unmarshal(data, &current); err == nil {
		if !isZeroStatisticsSnapshot(current.Statistics) {
			return current.Statistics, nil
		}
	}

	var legacy legacyPersistedSnapshot
	if err := json.Unmarshal(data, &legacy); err != nil {
		return StatisticsSnapshot{}, err
	}
	if !isZeroStatisticsSnapshot(legacy.Statistics) {
		return legacy.Statistics, nil
	}
	return legacy.Usage, nil
}

func isZeroStatisticsSnapshot(snapshot StatisticsSnapshot) bool {
	return snapshot.TotalRequests == 0 &&
		snapshot.SuccessCount == 0 &&
		snapshot.FailureCount == 0 &&
		snapshot.TotalTokens == 0 &&
		len(snapshot.APIs) == 0 &&
		len(snapshot.RequestsByDay) == 0 &&
		len(snapshot.RequestsByHour) == 0 &&
		len(snapshot.TokensByDay) == 0 &&
		len(snapshot.TokensByHour) == 0
}
