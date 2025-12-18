package metrics

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultRetentionDays is the default number of days to retain metrics.
const DefaultRetentionDays = 7

// Store handles JSON file-based metrics persistence.
type Store struct {
	dir           string
	mu            sync.RWMutex
	cache         map[string]*DailyMetrics
	retentionDays int
	stopPurge     chan struct{}
}

// NewStore creates a new metrics store at the given directory.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create metrics directory: %w", err)
	}
	s := &Store{
		dir:           dir,
		cache:         make(map[string]*DailyMetrics),
		retentionDays: DefaultRetentionDays,
		stopPurge:     make(chan struct{}),
	}
	// Purge old metrics on startup
	s.PruneOldMetrics(s.retentionDays)
	return s, nil
}

// NewStoreWithRetention creates a new metrics store with custom retention days.
func NewStoreWithRetention(dir string, retentionDays int) (*Store, error) {
	s, err := NewStore(dir)
	if err != nil {
		return nil, err
	}
	s.SetRetentionDays(retentionDays)
	return s, nil
}

// dateKey returns the date string (YYYY-MM-DD) for a given time.
func dateKey(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

// filePath returns the JSON file path for a given date.
func (s *Store) filePath(date string) string {
	return filepath.Join(s.dir, date+".json")
}

// WriteRecords writes multiple records to storage, grouped by day.
func (s *Store) WriteRecords(records []RequestRecord) error {
	if len(records) == 0 {
		return nil
	}

	byDay := make(map[string][]RequestRecord)
	for _, r := range records {
		day := dateKey(r.Timestamp)
		byDay[day] = append(byDay[day], r)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for day, dayRecords := range byDay {
		metrics, err := s.loadDayLocked(day)
		if err != nil {
			metrics = NewDailyMetrics(day)
		}

		for _, r := range dayRecords {
			s.addRecord(metrics, r)
		}

		if err := s.saveDayLocked(day, metrics); err != nil {
			return err
		}
		s.cache[day] = metrics
	}

	return nil
}

// addRecord adds a single record to the daily metrics.
func (s *Store) addRecord(dm *DailyMetrics, r RequestRecord) {
	dm.Requests = append(dm.Requests, r)
	dm.TotalCount++
	dm.TotalLatency += r.LatencyMs
	if dm.Histogram == nil {
		dm.Histogram = NewLatencyHistogram()
	}
	dm.Histogram.Record(r.LatencyMs)

	if !r.Success {
		dm.FailureCount++
	}

	if r.Provider != "" {
		ps, ok := dm.ByProvider[r.Provider]
		if !ok {
			ps = &ProviderStats{Histogram: NewLatencyHistogram()}
			dm.ByProvider[r.Provider] = ps
		}
		ps.Requests++
		ps.Histogram.Record(r.LatencyMs)
		if !r.Success {
			ps.Failures++
		}
	}

	if r.RequestType != "" {
		typeKey := string(r.RequestType)
		ts, ok := dm.ByType[typeKey]
		if !ok {
			ts = &TypeStats{}
			dm.ByType[typeKey] = ts
		}
		ts.Requests++
		if !r.Success {
			ts.Failures++
		}
	}

	if r.Profile != "" {
		ps, ok := dm.ByProfile[r.Profile]
		if !ok {
			ps = &ProfileStats{}
			dm.ByProfile[r.Profile] = ps
		}
		ps.Requests++
	}
}

// loadDayLocked loads metrics for a specific day (caller must hold lock).
func (s *Store) loadDayLocked(date string) (*DailyMetrics, error) {
	if cached, ok := s.cache[date]; ok {
		return cached, nil
	}

	path := s.filePath(date)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, err
		}
		return nil, fmt.Errorf("failed to read metrics file: %w", err)
	}

	var dm DailyMetrics
	if err := json.Unmarshal(data, &dm); err != nil {
		return nil, fmt.Errorf("failed to parse metrics file: %w", err)
	}

	if dm.ByProvider == nil {
		dm.ByProvider = make(map[string]*ProviderStats)
	}
	if dm.ByType == nil {
		dm.ByType = make(map[string]*TypeStats)
	}
	if dm.ByProfile == nil {
		dm.ByProfile = make(map[string]*ProfileStats)
	}
	if dm.Histogram == nil {
		dm.Histogram = NewLatencyHistogram()
	}

	return &dm, nil
}

// saveDayLocked saves metrics for a specific day (caller must hold lock).
func (s *Store) saveDayLocked(date string, dm *DailyMetrics) error {
	path := s.filePath(date)
	data, err := json.MarshalIndent(dm, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metrics: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write metrics file: %w", err)
	}
	return nil
}

// LoadMetrics loads and aggregates metrics for a time range.
func (s *Store) LoadMetrics(from, to time.Time) (*MetricsResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	fromDate := dateKey(from)
	toDate := dateKey(to)

	resp := &MetricsResponse{
		Period: Period{
			From: from.UTC().Format(time.RFC3339),
			To:   to.UTC().Format(time.RFC3339),
		},
		ByProvider: make(map[string]ProviderSummary),
		ByType:     make(map[string]TypeSummary),
		ByProfile:  make(map[string]ProfileSummary),
		Daily:      make([]DailySummary, 0),
	}

	var totalRequests, totalFailures, totalLatency int64
	globalHistogram := NewLatencyHistogram()
	providerHistograms := make(map[string]*LatencyHistogram)

	current := from
	for !current.After(to) {
		date := dateKey(current)
		if date >= fromDate && date <= toDate {
			dm, err := s.loadDayLocked(date)
			if err == nil && dm != nil {
				totalRequests += dm.TotalCount
				totalFailures += dm.FailureCount
				totalLatency += dm.TotalLatency

				if dm.Histogram != nil {
					globalHistogram.Merge(dm.Histogram)
				}

				for provider, ps := range dm.ByProvider {
					if _, ok := providerHistograms[provider]; !ok {
						providerHistograms[provider] = NewLatencyHistogram()
					}
					if ps.Histogram != nil {
						providerHistograms[provider].Merge(ps.Histogram)
					}

					existing := resp.ByProvider[provider]
					existing.Requests += ps.Requests
					existing.Failures += ps.Failures
					resp.ByProvider[provider] = existing
				}

				for reqType, ts := range dm.ByType {
					existing := resp.ByType[reqType]
					existing.Requests += ts.Requests
					existing.Failures += ts.Failures
					resp.ByType[reqType] = existing
				}

				for profile, ps := range dm.ByProfile {
					existing := resp.ByProfile[profile]
					existing.Requests += ps.Requests
					resp.ByProfile[profile] = existing
				}

				var avgLatency float64
				if dm.TotalCount > 0 {
					avgLatency = float64(dm.TotalLatency) / float64(dm.TotalCount)
				}
				resp.Daily = append(resp.Daily, DailySummary{
					Date:         date,
					Requests:     dm.TotalCount,
					Failures:     dm.FailureCount,
					AvgLatencyMs: avgLatency,
				})
			}
		}
		current = current.AddDate(0, 0, 1)
	}

	resp.Summary = Summary{
		TotalRequests: totalRequests,
		TotalFailures: totalFailures,
	}
	if totalRequests > 0 {
		resp.Summary.AvgLatencyMs = float64(totalLatency) / float64(totalRequests)
	}

	for provider, hist := range providerHistograms {
		existing := resp.ByProvider[provider]
		existing.P50Ms = hist.P50()
		existing.P90Ms = hist.P90()
		existing.P99Ms = hist.P99()
		resp.ByProvider[provider] = existing
	}

	sort.Slice(resp.Daily, func(i, j int) bool {
		return resp.Daily[i].Date < resp.Daily[j].Date
	})

	return resp, nil
}

// PruneOldMetrics removes metrics files older than retentionDays.
func (s *Store) PruneOldMetrics(retentionDays int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)
	cutoffDate := dateKey(cutoff)

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("failed to read metrics directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		date := strings.TrimSuffix(name, ".json")
		if date < cutoffDate {
			path := filepath.Join(s.dir, name)
			if err := os.Remove(path); err != nil {
			}
			delete(s.cache, date)
		}
	}

	return nil
}

// GetDirectory returns the metrics storage directory.
func (s *Store) GetDirectory() string {
	return s.dir
}

// GetRetentionDays returns the current retention period in days.
func (s *Store) GetRetentionDays() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.retentionDays
}

// SetRetentionDays sets the retention period. Minimum is 1 day.
func (s *Store) SetRetentionDays(days int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if days < 1 {
		days = 1
	}
	s.retentionDays = days
}

// GetMetricsSince returns metrics from the last duration.
func (s *Store) GetMetricsSince(duration time.Duration) (*MetricsResponse, error) {
	now := time.Now().UTC()
	from := now.Add(-duration)
	return s.LoadMetrics(from, now)
}

// StartBackgroundPurge starts a daily background purge task.
func (s *Store) StartBackgroundPurge() {
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.mu.RLock()
				retention := s.retentionDays
				s.mu.RUnlock()
				s.PruneOldMetrics(retention)
			case <-s.stopPurge:
				return
			}
		}
	}()
}

// StopBackgroundPurge stops the background purge task.
func (s *Store) StopBackgroundPurge() {
	select {
	case <-s.stopPurge:
		// Already closed
	default:
		close(s.stopPurge)
	}
}
