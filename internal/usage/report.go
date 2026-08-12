package usage

import (
	"sort"
	"strings"
	"time"
)

// Filter limits aggregate reports. From and To are inclusive local calendar days.
type Filter struct {
	From        time.Time
	To          time.Time
	ClientKeyID string
	Provider    string
	Model       string
	ServiceTier string
}

// UsageSummary adds time bounds and precomputed averages to additive metrics.
type UsageSummary struct {
	Metrics
	FirstUsedAt      time.Time `json:"first_used_at,omitempty"`
	LastUsedAt       time.Time `json:"last_used_at,omitempty"`
	AverageLatencyMs int64     `json:"average_latency_ms"`
	AverageTTFTMs    int64     `json:"average_ttft_ms"`
}

// ClientKeyUsage groups matched usage by safe client-key identity.
type ClientKeyUsage struct {
	KeyID     string       `json:"key_id"`
	Alias     string       `json:"alias,omitempty"`
	MaskedKey string       `json:"masked_key,omitempty"`
	Summary   UsageSummary `json:"summary"`
}

// DailyUsage groups matched usage by server-local date.
type DailyUsage struct {
	Day     string       `json:"day"`
	Summary UsageSummary `json:"summary"`
}

// ModelUsage groups matched usage by upstream provider/model/tier.
type ModelUsage struct {
	Provider    string       `json:"provider"`
	Model       string       `json:"model"`
	ServiceTier string       `json:"service_tier"`
	Summary     UsageSummary `json:"summary"`
}

// RecentUsage groups matched final requests and upstream attempts into a
// short-lived time window.
// It is held in memory only and intentionally never becomes part of a snapshot.
type RecentUsage struct {
	StartAt time.Time    `json:"start_at"`
	EndAt   time.Time    `json:"end_at"`
	Summary UsageSummary `json:"summary"`
}

// Report is the management-facing aggregate response.
type Report struct {
	Enabled          bool             `json:"enabled"`
	Estimated        bool             `json:"estimated"`
	Currency         string           `json:"currency,omitempty"`
	Currencies       []string         `json:"currencies"`
	RetentionDays    int              `json:"retention_days"`
	BucketCount      int              `json:"bucket_count"`
	Summary          UsageSummary     `json:"summary"`
	Keys             []ClientKeyUsage `json:"keys"`
	Daily            []DailyUsage     `json:"daily"`
	Models           []ModelUsage     `json:"models"`
	Recent           []RecentUsage    `json:"recent"`
	RecentWindowMins int              `json:"recent_window_minutes"`
	Overflow         UsageSummary     `json:"overflow"`
	PersistenceError string           `json:"persistence_error,omitempty"`
}

// ExportRow is one safe aggregate row for CSV and programmatic exports.
type ExportRow struct {
	Bucket
	Alias     string `json:"alias,omitempty"`
	MaskedKey string `json:"masked_key,omitempty"`
	Overflow  bool   `json:"overflow,omitempty"`
}

// Report returns a filtered, deterministic view of retained aggregate buckets.
func (c *Collector) Report(filter Filter) Report {
	if c == nil {
		return Report{Estimated: true, Currencies: []string{}, Keys: []ClientKeyUsage{}, Daily: []DailyUsage{}, Models: []ModelUsage{}, Recent: []RecentUsage{}, RecentWindowMins: recentWindowMinutes}
	}
	snapshot := c.ExportSnapshot()
	c.mu.RLock()
	enabled := c.enabled
	currency := ""
	if c.pricing != nil {
		currency = c.pricing.currency
	}
	recent := c.recentSnapshotLocked()
	c.mu.RUnlock()
	infoByID := make(map[string]ClientKeyInfo, len(snapshot.ClientKeys))
	for _, info := range snapshot.ClientKeys {
		infoByID[info.ID] = info
	}
	keyGroups := make(map[string]*ClientKeyUsage)
	dailyGroups := make(map[string]*DailyUsage)
	modelGroups := make(map[string]*ModelUsage)
	recentGroups := make(map[time.Time]*RecentUsage)
	report := Report{
		Enabled:          enabled,
		Estimated:        true,
		RetentionDays:    snapshot.RetentionDays,
		Currencies:       make([]string, 0),
		Keys:             make([]ClientKeyUsage, 0),
		Daily:            make([]DailyUsage, 0),
		Models:           make([]ModelUsage, 0),
		Recent:           make([]RecentUsage, 0),
		RecentWindowMins: recentWindowMinutes,
	}
	if errPersistence := c.PersistenceError(); errPersistence != nil {
		report.PersistenceError = errPersistence.Error()
	}

	for _, bucket := range snapshot.Buckets {
		if !filter.matches(bucket) {
			continue
		}
		report.BucketCount++
		mergeUsageSummary(&report.Summary, bucket)

		keyGroup := keyGroups[bucket.ClientKeyID]
		if keyGroup == nil {
			info := infoByID[bucket.ClientKeyID]
			keyGroup = &ClientKeyUsage{
				KeyID:     bucket.ClientKeyID,
				Alias:     info.Alias,
				MaskedKey: info.MaskedKey,
			}
			keyGroups[bucket.ClientKeyID] = keyGroup
		}
		mergeUsageSummary(&keyGroup.Summary, bucket)

		dailyGroup := dailyGroups[bucket.Day]
		if dailyGroup == nil {
			dailyGroup = &DailyUsage{Day: bucket.Day}
			dailyGroups[bucket.Day] = dailyGroup
		}
		mergeUsageSummary(&dailyGroup.Summary, bucket)

		modelKey := strings.Join([]string{bucket.Provider, bucket.Model, bucket.ServiceTier}, "\x00")
		modelGroup := modelGroups[modelKey]
		if modelGroup == nil {
			modelGroup = &ModelUsage{
				Provider:    bucket.Provider,
				Model:       bucket.Model,
				ServiceTier: bucket.ServiceTier,
			}
			modelGroups[modelKey] = modelGroup
		}
		mergeUsageSummary(&modelGroup.Summary, bucket)
	}

	for _, bucket := range recent {
		if !filter.matches(Bucket{
			Day:         c.usageDay(bucket.StartAt),
			ClientKeyID: bucket.ClientKeyID,
			Provider:    bucket.Provider,
			Model:       bucket.Model,
			ServiceTier: bucket.ServiceTier,
		}) {
			continue
		}
		group := recentGroups[bucket.StartAt]
		if group == nil {
			group = &RecentUsage{StartAt: bucket.StartAt, EndAt: bucket.StartAt.Add(recentBucketDuration)}
			recentGroups[bucket.StartAt] = group
		}
		mergeMetrics(&group.Summary.Metrics, bucket.Metrics)
	}

	if strings.TrimSpace(filter.ClientKeyID) == "" && strings.TrimSpace(filter.Provider) == "" && strings.TrimSpace(filter.Model) == "" && strings.TrimSpace(filter.ServiceTier) == "" {
		for _, overflow := range snapshot.Overflow {
			if !filter.matchesDay(overflow.Day) {
				continue
			}
			mergeMetrics(&report.Overflow.Metrics, overflow.Metrics)
			mergeMetrics(&report.Summary.Metrics, overflow.Metrics)
		}
		finalizeSummary(&report.Overflow)
	}
	finalizeSummary(&report.Summary)
	report.Currencies = sortedCurrencies(report.Summary.EstimatedCostMicrosByCurrency)
	if len(report.Currencies) == 1 {
		report.Currency = report.Currencies[0]
	} else if len(report.Currencies) == 0 {
		report.Currency = currency
	}
	for _, group := range keyGroups {
		finalizeSummary(&group.Summary)
		report.Keys = append(report.Keys, *group)
	}
	for _, group := range dailyGroups {
		finalizeSummary(&group.Summary)
		report.Daily = append(report.Daily, *group)
	}
	for _, group := range modelGroups {
		finalizeSummary(&group.Summary)
		report.Models = append(report.Models, *group)
	}
	for _, group := range recentGroups {
		finalizeSummary(&group.Summary)
		report.Recent = append(report.Recent, *group)
	}
	sort.Slice(report.Keys, func(i, j int) bool { return report.Keys[i].KeyID < report.Keys[j].KeyID })
	sort.Slice(report.Daily, func(i, j int) bool { return report.Daily[i].Day < report.Daily[j].Day })
	sort.Slice(report.Models, func(i, j int) bool {
		left, right := report.Models[i], report.Models[j]
		if left.Provider != right.Provider {
			return left.Provider < right.Provider
		}
		if left.Model != right.Model {
			return left.Model < right.Model
		}
		return left.ServiceTier < right.ServiceTier
	})
	sort.Slice(report.Recent, func(i, j int) bool { return report.Recent[i].StartAt.Before(report.Recent[j].StartAt) })
	return report
}

func sortedCurrencies(costs map[string]int64) []string {
	currencies := make([]string, 0, len(costs))
	for currency := range costs {
		currencies = append(currencies, currency)
	}
	sort.Strings(currencies)
	return currencies
}

// ExportRows returns the filtered aggregate buckets with safe key metadata.
func (c *Collector) ExportRows(filter Filter) []ExportRow {
	if c == nil {
		return []ExportRow{}
	}
	snapshot := c.ExportSnapshot()
	infoByID := make(map[string]ClientKeyInfo, len(snapshot.ClientKeys))
	for _, info := range snapshot.ClientKeys {
		infoByID[info.ID] = info
	}
	rows := make([]ExportRow, 0, len(snapshot.Buckets))
	for _, bucket := range snapshot.Buckets {
		if !filter.matches(bucket) {
			continue
		}
		info := infoByID[bucket.ClientKeyID]
		rows = append(rows, ExportRow{Bucket: bucket, Alias: info.Alias, MaskedKey: info.MaskedKey})
	}
	if strings.TrimSpace(filter.ClientKeyID) == "" && strings.TrimSpace(filter.Provider) == "" && strings.TrimSpace(filter.Model) == "" && strings.TrimSpace(filter.ServiceTier) == "" {
		for _, overflow := range snapshot.Overflow {
			if filter.matchesDay(overflow.Day) {
				rows = append(rows, ExportRow{Bucket: Bucket{Day: overflow.Day, Metrics: overflow.Metrics}, Overflow: true})
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Day != rows[j].Day {
			return rows[i].Day < rows[j].Day
		}
		return !rows[i].Overflow && rows[j].Overflow
	})
	return rows
}

// ExportSnapshotFiltered returns the versioned snapshot restricted to the same
// safe aggregate filters used by management reports.
func (c *Collector) ExportSnapshotFiltered(filter Filter) Snapshot {
	if c == nil {
		return Snapshot{Version: SnapshotVersion, Buckets: []Bucket{}, ClientKeys: []ClientKeyInfo{}}
	}
	snapshot := c.ExportSnapshot()
	if filter.isZero() {
		return snapshot
	}
	filteredBuckets := make([]Bucket, 0, len(snapshot.Buckets))
	referencedKeys := make(map[string]struct{})
	for _, bucket := range snapshot.Buckets {
		if !filter.matches(bucket) {
			continue
		}
		filteredBuckets = append(filteredBuckets, bucket)
		referencedKeys[bucket.ClientKeyID] = struct{}{}
	}
	filteredKeys := make([]ClientKeyInfo, 0, len(referencedKeys))
	for _, info := range snapshot.ClientKeys {
		if _, exists := referencedKeys[info.ID]; exists {
			filteredKeys = append(filteredKeys, info)
		}
	}
	filteredOverflow := make([]OverflowBucket, 0)
	if strings.TrimSpace(filter.ClientKeyID) == "" && strings.TrimSpace(filter.Provider) == "" && strings.TrimSpace(filter.Model) == "" && strings.TrimSpace(filter.ServiceTier) == "" {
		for _, overflow := range snapshot.Overflow {
			if filter.matchesDay(overflow.Day) {
				filteredOverflow = append(filteredOverflow, overflow)
			}
		}
	}
	snapshot.Buckets = filteredBuckets
	snapshot.ClientKeys = filteredKeys
	snapshot.Overflow = filteredOverflow
	return snapshot
}

func (filter Filter) matches(bucket Bucket) bool {
	if !filter.matchesDay(bucket.Day) {
		return false
	}
	if value := strings.TrimSpace(filter.ClientKeyID); value != "" && bucket.ClientKeyID != value {
		return false
	}
	if value := strings.TrimSpace(filter.Provider); value != "" && !strings.EqualFold(bucket.Provider, value) {
		return false
	}
	if value := strings.TrimSpace(filter.Model); value != "" && !strings.EqualFold(bucket.Model, value) {
		return false
	}
	if value := strings.TrimSpace(filter.ServiceTier); value != "" && !strings.EqualFold(bucket.ServiceTier, value) {
		return false
	}
	return true
}

func (filter Filter) matchesDay(day string) bool {
	if !filter.From.IsZero() && day < filter.From.UTC().Format(time.DateOnly) {
		return false
	}
	if !filter.To.IsZero() && day > filter.To.UTC().Format(time.DateOnly) {
		return false
	}
	return true
}

func (filter Filter) isZero() bool {
	return filter.From.IsZero() && filter.To.IsZero() && strings.TrimSpace(filter.ClientKeyID) == "" &&
		strings.TrimSpace(filter.Provider) == "" && strings.TrimSpace(filter.Model) == "" && strings.TrimSpace(filter.ServiceTier) == ""
}

func mergeUsageSummary(summary *UsageSummary, bucket Bucket) {
	if summary == nil {
		return
	}
	mergeMetrics(&summary.Metrics, bucket.Metrics)
	if summary.FirstUsedAt.IsZero() || (!bucket.FirstUsedAt.IsZero() && bucket.FirstUsedAt.Before(summary.FirstUsedAt)) {
		summary.FirstUsedAt = bucket.FirstUsedAt
	}
	if bucket.LastUsedAt.After(summary.LastUsedAt) {
		summary.LastUsedAt = bucket.LastUsedAt
	}
}

func finalizeSummary(summary *UsageSummary) {
	if summary == nil {
		return
	}
	summary.AverageLatencyMs = summary.Metrics.AverageLatencyMs()
	summary.AverageTTFTMs = summary.Metrics.AverageTTFTMs()
}
