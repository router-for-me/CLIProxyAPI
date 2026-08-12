package usage

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

// CollectorOptions customizes collector persistence and resource bounds.
type CollectorOptions struct {
	Store         Store
	FlushInterval time.Duration
	MaxBuckets    int
	MaxClientKeys int
	Now           func() time.Time
	DayLocation   *time.Location
}

// Collector is a bounded daily usage aggregator and core usage plugin.
type Collector struct {
	mu             sync.RWMutex
	buckets        map[bucketKey]*Bucket
	recentBuckets  map[recentBucketKey]*recentBucket
	clientKeys     map[string]*ClientKeyInfo
	configuredKeys map[string]clientKeyDisplay
	secretRedactor *strings.Replacer
	overflow       map[string]*Metrics
	enabled        bool
	retentionDays  int
	pricing        *pricingTable
	maxBuckets     int
	maxClientKeys  int
	now            func() time.Time
	dayLocation    *time.Location
	dirty          bool
	mutation       uint64
	lastPrunedDay  string

	store              Store
	flushMu            sync.Mutex
	flushInterval      time.Duration
	lifecycleMu        sync.Mutex
	loaded             bool
	started            bool
	closed             bool
	persistenceBlocked bool
	persistenceErr     error
	stop               chan struct{}
	done               chan struct{}
	stopOnce           sync.Once
}

// NewCollector creates a collector backed by a JSON snapshot at storagePath.
// An empty path creates an in-memory collector.
func NewCollector(cfg *config.Config, storagePath string) *Collector {
	var store Store
	if strings.TrimSpace(storagePath) != "" {
		store = NewFileStore(storagePath)
	}
	return NewCollectorWithOptions(cfg, CollectorOptions{Store: store})
}

// NewCollectorWithOptions creates a collector with injectable persistence and
// clock behavior for embedding and tests.
func NewCollectorWithOptions(cfg *config.Config, options CollectorOptions) *Collector {
	flushInterval := options.FlushInterval
	if flushInterval <= 0 {
		flushInterval = defaultFlushInterval
	}
	maxBuckets := options.MaxBuckets
	if maxBuckets <= 0 {
		maxBuckets = DefaultMaxBuckets
	}
	maxClientKeys := options.MaxClientKeys
	if maxClientKeys <= 0 {
		maxClientKeys = DefaultMaxClientKeys
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	dayLocation := options.DayLocation
	if dayLocation == nil {
		dayLocation = time.Local
	}
	c := &Collector{
		buckets:       make(map[bucketKey]*Bucket),
		recentBuckets: make(map[recentBucketKey]*recentBucket),
		clientKeys:    make(map[string]*ClientKeyInfo),
		overflow:      make(map[string]*Metrics),
		maxBuckets:    maxBuckets,
		maxClientKeys: maxClientKeys,
		now:           now,
		dayLocation:   dayLocation,
		store:         options.Store,
		flushInterval: flushInterval,
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
	}
	c.ApplyConfig(cfg)
	return c
}

// ApplyConfig updates collection, retention, and pricing. Existing aggregates
// are repriced whenever the pricing table changes so historical totals use the
// same billing rules as new attempts.
func (c *Collector) ApplyConfig(cfg *config.Config) {
	if c == nil {
		return
	}
	retentionDays := config.DefaultUsageStatisticsRetentionDays
	enabled := false
	pricingConfig := config.UsagePricingConfig{
		Currency: config.DefaultUsagePricingCurrency,
		Version:  config.DefaultUsagePricingVersion,
	}
	if cfg != nil {
		enabled = cfg.UsageStatisticsEnabled
		retentionDays = cfg.UsageStatisticsRetentionDays
		if retentionDays <= 0 {
			retentionDays = config.DefaultUsageStatisticsRetentionDays
		} else if retentionDays > config.MaxUsageStatisticsRetentionDays {
			retentionDays = config.MaxUsageStatisticsRetentionDays
		}
		pricingConfig = cfg.UsagePricing
	}
	configuredKeys, secretRedactor := configuredClientKeyDisplays(cfg)
	nextPricing := compilePricing(pricingConfig)

	c.mu.Lock()
	pricingChanged := c.pricing == nil || c.pricing.version != nextPricing.version || c.pricing.currency != nextPricing.currency
	c.enabled = enabled
	c.retentionDays = retentionDays
	c.pricing = nextPricing
	c.configuredKeys = configuredKeys
	c.secretRedactor = secretRedactor
	c.sanitizePricingSecretsLocked(pricingConfig)
	secretMetadataChanged := c.sanitizeStoredClientMetadataLocked()
	metadataChanged := c.refreshClientKeyMetadataLocked()
	repriced := pricingChanged && c.repriceAllLocked()
	changed := c.pruneLocked(c.now().UTC())
	if changed || metadataChanged || secretMetadataChanged || repriced {
		c.markDirtyLocked()
	}
	c.mu.Unlock()
}

// Enabled reports whether new records are currently collected.
func (c *Collector) Enabled() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.enabled
}

// Load restores the configured store once. Missing stores and files are treated
// as empty state by their Store implementation.
func (c *Collector) Load(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	return c.loadLocked(ctx)
}

func (c *Collector) loadLocked(ctx context.Context) error {
	if c.loaded {
		return nil
	}
	if c.store == nil {
		c.loaded = true
		return nil
	}
	snapshot, errLoad := c.store.Load(ctx)
	if errLoad != nil {
		c.persistenceBlocked = true
		c.persistenceErr = errLoad
		return errLoad
	}
	if snapshot.Version != 0 && snapshot.Version != SnapshotVersion {
		c.persistenceBlocked = true
		c.persistenceErr = ErrUnsupportedSnapshotVersion
		return ErrUnsupportedSnapshotVersion
	}
	c.mu.Lock()
	c.restoreLocked(snapshot)
	c.mu.Unlock()
	c.loaded = true
	return nil
}

// Start loads persisted state and starts the dirty-snapshot flush loop.
func (c *Collector) Start(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.closed {
		return errors.New("usage collector is closed")
	}
	if c.started {
		return nil
	}
	if errLoad := c.loadLocked(ctx); errLoad != nil {
		return errLoad
	}
	c.started = true
	go c.flushLoop()
	return nil
}

func (c *Collector) flushLoop() {
	ticker := time.NewTicker(c.flushInterval)
	defer func() {
		ticker.Stop()
		close(c.done)
	}()
	for {
		select {
		case <-ticker.C:
			if errFlush := c.Flush(context.Background()); errFlush != nil {
				log.WithError(errFlush).Warn("usage: persist aggregate snapshot")
			}
		case <-c.stop:
			return
		}
	}
}

// Close stops the flush worker and saves the latest dirty snapshot.
func (c *Collector) Close(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.lifecycleMu.Lock()
	started := c.started
	c.closed = true
	c.lifecycleMu.Unlock()
	if started {
		c.stopOnce.Do(func() { close(c.stop) })
		select {
		case <-c.done:
		case <-contextDone(ctx):
			if ctx != nil {
				return ctx.Err()
			}
		}
	}
	return c.Flush(ctx)
}

func contextDone(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	return ctx.Done()
}

// HandleUsage implements sdk/cliproxy/usage.Plugin.
func (c *Collector) HandleUsage(_ context.Context, record coreusage.Record) {
	if c == nil {
		return
	}
	now := c.now().UTC()
	timestamp := record.RequestedAt.UTC()
	if timestamp.IsZero() || timestamp.After(now) {
		timestamp = now
	}
	clientKeyID, alias, maskedKey := clientIdentity(record)
	provider := normalizedDimension(record.Provider)
	model := normalizedDimension(record.Model)
	serviceTier := strings.TrimSpace(record.ServiceTier)
	if serviceTier == "" {
		serviceTier = coreusage.DefaultServiceTier
	}
	serviceTier = sanitizeDisplayValue(serviceTier, 128)

	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabled {
		return
	}
	rawKey := strings.TrimSpace(record.APIKey)
	sanitizedClientKeyID, sanitizedAlias := c.sanitizeConfiguredSecretsLocked(clientKeyID, alias)
	if sanitizedClientKeyID != clientKeyID && rawKey != "" {
		clientKeyID = sdkaccess.FallbackClientKeyID(rawKey)
	} else {
		clientKeyID = sanitizedClientKeyID
	}
	alias = sanitizedAlias
	provider = c.sanitizePersistedDimensionLocked(provider, rawKey, unknownDimension)
	model = c.sanitizePersistedDimensionLocked(model, rawKey, unknownDimension)
	serviceTier = c.sanitizePersistedDimensionLocked(serviceTier, rawKey, coreusage.DefaultServiceTier)
	day := c.usageDay(timestamp)
	currentDay := c.usageDay(now)
	if c.lastPrunedDay != currentDay {
		if c.pruneLocked(now) {
			c.markDirtyLocked()
		}
		c.lastPrunedDay = currentDay
	}
	cutoff := now.In(c.dayLocation).AddDate(0, 0, -(c.retentionDays - 1)).Format(time.DateOnly)
	if day < cutoff {
		return
	}

	pricingRecord := record
	pricingRecord.Provider = provider
	pricingRecord.Model = model
	pricingRecord.ServiceTier = serviceTier
	delta := metricsForRecord(pricingRecord, c.pricingForClientLocked(clientKeyID))
	c.recordRecentLocked(timestamp, clientKeyID, provider, model, serviceTier, delta)
	key := bucketKey{
		day:         day,
		clientKeyID: clientKeyID,
		provider:    provider,
		model:       model,
		serviceTier: serviceTier,
	}
	bucket := c.buckets[key]
	if bucket == nil && len(c.buckets) >= c.maxBuckets {
		delta.OverflowAttempts = saturatingAdd(delta.OverflowAttempts, 1)
		overflow := c.overflow[day]
		if overflow == nil {
			overflow = &Metrics{}
			c.overflow[day] = overflow
		}
		mergeMetrics(overflow, delta)
		c.markDirtyLocked()
		return
	}
	if bucket == nil {
		bucket = &Bucket{
			Day:         day,
			ClientKeyID: clientKeyID,
			Provider:    provider,
			Model:       model,
			ServiceTier: serviceTier,
			FirstUsedAt: timestamp,
			LastUsedAt:  timestamp,
		}
		c.buckets[key] = bucket
	}
	mergeMetrics(&bucket.Metrics, delta)
	if bucket.FirstUsedAt.IsZero() || timestamp.Before(bucket.FirstUsedAt) {
		bucket.FirstUsedAt = timestamp
	}
	if timestamp.After(bucket.LastUsedAt) {
		bucket.LastUsedAt = timestamp
	}
	c.updateClientKeyLocked(clientKeyID, alias, maskedKey, timestamp)
	c.markDirtyLocked()
}

func (c *Collector) pricingForClientLocked(_ string) *pricingTable {
	return c.pricing
}

func (c *Collector) updateClientKeyLocked(id, alias, maskedKey string, timestamp time.Time) {
	if display, configured := c.configuredKeys[id]; configured {
		alias = display.alias
		maskedKey = display.maskedKey
	}
	info := c.clientKeys[id]
	if info == nil {
		if len(c.clientKeys) >= c.maxClientKeys {
			return
		}
		info = &ClientKeyInfo{ID: id, FirstUsedAt: timestamp, LastUsedAt: timestamp}
		c.clientKeys[id] = info
	}
	if alias != "" {
		info.Alias = alias
	}
	if maskedKey != "" {
		info.MaskedKey = maskedKey
	}
	if info.FirstUsedAt.IsZero() || timestamp.Before(info.FirstUsedAt) {
		info.FirstUsedAt = timestamp
	}
	if timestamp.After(info.LastUsedAt) {
		info.LastUsedAt = timestamp
	}
}

func (c *Collector) recordRecentLocked(timestamp time.Time, clientKeyID, provider, model, serviceTier string, delta Metrics) {
	if c == nil {
		return
	}
	start := timestamp.UTC().Truncate(recentBucketDuration)
	key := recentBucketKey{
		startUnix:   start.Unix(),
		clientKeyID: clientKeyID,
		provider:    provider,
		model:       model,
		serviceTier: serviceTier,
	}
	bucket := c.recentBuckets[key]
	if bucket == nil {
		bucket = &recentBucket{
			StartAt:     start,
			ClientKeyID: clientKeyID,
			Provider:    provider,
			Model:       model,
			ServiceTier: serviceTier,
		}
		c.recentBuckets[key] = bucket
	}
	mergeMetrics(&bucket.Metrics, delta)
	c.pruneRecentLocked(c.now().UTC())
}

func (c *Collector) pruneRecentLocked(now time.Time) {
	if c == nil || len(c.recentBuckets) == 0 {
		return
	}
	cutoff := now.UTC().Truncate(recentBucketDuration).Add(-time.Duration(recentBucketCount-1) * recentBucketDuration)
	for key, bucket := range c.recentBuckets {
		if bucket.StartAt.Before(cutoff) || bucket.StartAt.After(now.UTC()) {
			delete(c.recentBuckets, key)
		}
	}
}

func (c *Collector) recentSnapshotLocked() []recentBucket {
	if c == nil || len(c.recentBuckets) == 0 {
		return []recentBucket{}
	}
	recent := make([]recentBucket, 0, len(c.recentBuckets))
	for _, bucket := range c.recentBuckets {
		copyBucket := *bucket
		copyBucket.Metrics = cloneMetrics(bucket.Metrics)
		recent = append(recent, copyBucket)
	}
	return recent
}

// ResetAllClientKeyUsage clears all retained client-key aggregates and persists
// the empty snapshot. Configured API keys and upstream credential state are not changed.
func (c *Collector) ResetAllClientKeyUsage(ctx context.Context) (int, error) {
	if c == nil {
		return 0, nil
	}
	c.mu.Lock()
	keyIDs := make(map[string]struct{}, len(c.clientKeys))
	for id := range c.clientKeys {
		keyIDs[id] = struct{}{}
	}
	for key := range c.buckets {
		keyIDs[key.clientKeyID] = struct{}{}
	}
	c.buckets = make(map[bucketKey]*Bucket)
	c.clientKeys = make(map[string]*ClientKeyInfo)
	c.overflow = make(map[string]*Metrics)
	c.recentBuckets = make(map[recentBucketKey]*recentBucket)
	c.lastPrunedDay = c.usageDay(c.now())
	c.markDirtyLocked()
	c.mu.Unlock()

	if errFlush := c.Flush(ctx); errFlush != nil {
		return len(keyIDs), errFlush
	}
	return len(keyIDs), nil
}

func (c *Collector) markDirtyLocked() {
	c.dirty = true
	c.mutation++
}

func (c *Collector) usageDay(value time.Time) string {
	location := c.dayLocation
	if location == nil {
		location = time.Local
	}
	return value.In(location).Format(time.DateOnly)
}

func (c *Collector) unambiguousUsageDay(first, last time.Time) (string, bool) {
	if first.IsZero() || last.IsZero() {
		return "", false
	}
	firstDay := c.usageDay(first)
	if firstDay != c.usageDay(last) {
		return "", false
	}
	return firstDay, true
}

func (c *Collector) pruneLocked(now time.Time) bool {
	retentionDays := c.retentionDays
	if retentionDays <= 0 {
		retentionDays = config.DefaultUsageStatisticsRetentionDays
	}
	localNow := now.In(c.dayLocation)
	cutoff := localNow.AddDate(0, 0, -(retentionDays - 1)).Format(time.DateOnly)
	latest := localNow.Format(time.DateOnly)
	changed := false
	referencedKeys := make(map[string]struct{})
	for key := range c.buckets {
		if key.day < cutoff || key.day > latest {
			delete(c.buckets, key)
			changed = true
			continue
		}
		referencedKeys[key.clientKeyID] = struct{}{}
	}
	for id := range c.clientKeys {
		if _, ok := referencedKeys[id]; !ok {
			delete(c.clientKeys, id)
			changed = true
		}
	}
	for day := range c.overflow {
		if day < cutoff || day > latest {
			delete(c.overflow, day)
			changed = true
		}
	}
	c.pruneRecentLocked(now)
	return changed
}

func (c *Collector) restoreLocked(snapshot Snapshot) {
	now := c.now().UTC()
	retentionDays := c.retentionDays
	if retentionDays <= 0 {
		retentionDays = config.DefaultUsageStatisticsRetentionDays
	}
	localNow := now.In(c.dayLocation)
	cutoff := localNow.AddDate(0, 0, -(retentionDays - 1)).Format(time.DateOnly)
	latest := localNow.Format(time.DateOnly)
	restoreChanged := false
	c.buckets = make(map[bucketKey]*Bucket)
	c.recentBuckets = make(map[recentBucketKey]*recentBucket)
	c.clientKeys = make(map[string]*ClientKeyInfo)
	c.overflow = make(map[string]*Metrics)
	for _, overflow := range snapshot.Overflow {
		if _, errDay := time.Parse(time.DateOnly, overflow.Day); errDay != nil {
			restoreChanged = true
			continue
		}
		if overflow.Day < cutoff || overflow.Day > latest {
			restoreChanged = true
			continue
		}
		metrics := cloneMetrics(overflow.Metrics)
		normalizeCurrencyCosts(&metrics)
		if c.sanitizeMetricsSecretsLocked(&metrics) {
			restoreChanged = true
		}
		if existing := c.overflow[overflow.Day]; existing != nil {
			mergeMetrics(existing, metrics)
		} else {
			c.overflow[overflow.Day] = &metrics
		}
	}
	for _, bucket := range snapshot.Buckets {
		bucket.Day = strings.TrimSpace(bucket.Day)
		if _, errDay := time.Parse(time.DateOnly, bucket.Day); errDay != nil {
			restoreChanged = true
			continue
		}
		if migratedDay, ok := c.unambiguousUsageDay(bucket.FirstUsedAt, bucket.LastUsedAt); ok && migratedDay != bucket.Day {
			bucket.Day = migratedDay
			restoreChanged = true
		}
		bucket.ClientKeyID = sanitizeIdentifier(bucket.ClientKeyID, anonymousClientKeyID)
		bucket.ClientKeyID, _ = c.sanitizeConfiguredSecretsLocked(bucket.ClientKeyID, "")
		bucket.Provider = c.sanitizePersistedDimensionLocked(normalizedDimension(bucket.Provider), "", unknownDimension)
		bucket.Model = c.sanitizePersistedDimensionLocked(normalizedDimension(bucket.Model), "", unknownDimension)
		bucket.ServiceTier = strings.TrimSpace(bucket.ServiceTier)
		if bucket.ServiceTier == "" {
			bucket.ServiceTier = coreusage.DefaultServiceTier
		}
		bucket.ServiceTier = sanitizeDisplayValue(bucket.ServiceTier, 128)
		bucket.ServiceTier = c.sanitizePersistedDimensionLocked(bucket.ServiceTier, "", coreusage.DefaultServiceTier)
		if bucket.Day < cutoff || bucket.Day > latest {
			restoreChanged = true
			continue
		}
		normalizeCurrencyCosts(&bucket.Metrics)
		if c.sanitizeMetricsSecretsLocked(&bucket.Metrics) {
			restoreChanged = true
		}
		key := keyForBucket(bucket)
		if existing := c.buckets[key]; existing != nil {
			mergeMetrics(&existing.Metrics, bucket.Metrics)
			if existing.FirstUsedAt.IsZero() || (!bucket.FirstUsedAt.IsZero() && bucket.FirstUsedAt.Before(existing.FirstUsedAt)) {
				existing.FirstUsedAt = bucket.FirstUsedAt
			}
			if bucket.LastUsedAt.After(existing.LastUsedAt) {
				existing.LastUsedAt = bucket.LastUsedAt
			}
			continue
		}
		if len(c.buckets) >= c.maxBuckets {
			restoreChanged = true
			bucket.Metrics.OverflowAttempts = saturatingAdd(bucket.Metrics.OverflowAttempts, bucket.Metrics.Attempts)
			overflow := c.overflow[bucket.Day]
			if overflow == nil {
				overflow = &Metrics{}
				c.overflow[bucket.Day] = overflow
			}
			mergeMetrics(overflow, bucket.Metrics)
			continue
		}
		copyBucket := bucket
		copyBucket.Metrics = cloneMetrics(bucket.Metrics)
		c.buckets[key] = &copyBucket
	}
	for _, info := range snapshot.ClientKeys {
		info.ID = sanitizeIdentifier(info.ID, "")
		info.ID, info.Alias = c.sanitizeConfiguredSecretsLocked(info.ID, info.Alias)
		if info.ID == "" || len(c.clientKeys) >= c.maxClientKeys {
			restoreChanged = true
			continue
		}
		info.Alias = sanitizeDisplayValue(info.Alias, 128)
		info.MaskedKey = sanitizeDisplayValue(info.MaskedKey, 64)
		if c.secretRedactor != nil && c.secretRedactor.Replace(info.MaskedKey) != info.MaskedKey {
			info.MaskedKey = ""
		}
		copyInfo := info
		c.clientKeys[info.ID] = &copyInfo
	}
	metadataChanged := c.refreshClientKeyMetadataLocked()
	repriced := c.repriceAllLocked()
	pruned := c.pruneLocked(now)
	c.dirty = restoreChanged || metadataChanged || repriced || pruned
	if c.dirty {
		c.mutation = 1
	} else {
		c.mutation = 0
	}
}

// ExportSnapshot returns a deterministic, secret-safe copy of current state.
func (c *Collector) ExportSnapshot() Snapshot {
	if c == nil {
		return Snapshot{Version: SnapshotVersion}
	}
	c.mu.Lock()
	if c.pruneLocked(c.now().UTC()) {
		c.markDirtyLocked()
	}
	snapshot := c.snapshotLocked()
	c.mu.Unlock()
	return snapshot
}

func (c *Collector) snapshotLocked() Snapshot {
	snapshot := Snapshot{
		Version:       SnapshotVersion,
		SavedAt:       c.now().UTC(),
		RetentionDays: c.retentionDays,
		Buckets:       make([]Bucket, 0, len(c.buckets)),
		ClientKeys:    make([]ClientKeyInfo, 0, len(c.clientKeys)),
		Overflow:      make([]OverflowBucket, 0, len(c.overflow)),
	}
	for _, bucket := range c.buckets {
		copyBucket := *bucket
		copyBucket.Metrics = cloneMetrics(bucket.Metrics)
		snapshot.Buckets = append(snapshot.Buckets, copyBucket)
	}
	for _, info := range c.clientKeys {
		snapshot.ClientKeys = append(snapshot.ClientKeys, *info)
	}
	for day, metrics := range c.overflow {
		snapshot.Overflow = append(snapshot.Overflow, OverflowBucket{Day: day, Metrics: cloneMetrics(*metrics)})
	}
	sort.Slice(snapshot.Buckets, func(i, j int) bool {
		left, right := snapshot.Buckets[i], snapshot.Buckets[j]
		if left.Day != right.Day {
			return left.Day < right.Day
		}
		if left.ClientKeyID != right.ClientKeyID {
			return left.ClientKeyID < right.ClientKeyID
		}
		if left.Provider != right.Provider {
			return left.Provider < right.Provider
		}
		if left.Model != right.Model {
			return left.Model < right.Model
		}
		return left.ServiceTier < right.ServiceTier
	})
	sort.Slice(snapshot.ClientKeys, func(i, j int) bool { return snapshot.ClientKeys[i].ID < snapshot.ClientKeys[j].ID })
	sort.Slice(snapshot.Overflow, func(i, j int) bool { return snapshot.Overflow[i].Day < snapshot.Overflow[j].Day })
	return snapshot
}

// Flush atomically persists the current dirty snapshot.
func (c *Collector) Flush(ctx context.Context) error {
	if c == nil || c.store == nil {
		return nil
	}
	c.flushMu.Lock()
	defer c.flushMu.Unlock()
	c.lifecycleMu.Lock()
	blocked := c.persistenceBlocked
	blockedErr := c.persistenceErr
	c.lifecycleMu.Unlock()
	if blocked {
		if blockedErr != nil {
			return blockedErr
		}
		return errors.New("usage persistence is blocked")
	}
	c.mu.Lock()
	if c.pruneLocked(c.now().UTC()) {
		c.markDirtyLocked()
	}
	if !c.dirty {
		c.mu.Unlock()
		return nil
	}
	snapshot := c.snapshotLocked()
	mutation := c.mutation
	c.mu.Unlock()

	if errSave := c.store.Save(ctx, snapshot); errSave != nil {
		c.lifecycleMu.Lock()
		if !c.persistenceBlocked {
			c.persistenceErr = errSave
		}
		c.lifecycleMu.Unlock()
		return errSave
	}
	c.lifecycleMu.Lock()
	if !c.persistenceBlocked {
		c.persistenceErr = nil
	}
	c.lifecycleMu.Unlock()
	c.mu.Lock()
	if c.mutation == mutation {
		c.dirty = false
	}
	c.mu.Unlock()
	return nil
}

func metricsForRecord(record coreusage.Record, pricing *pricingTable) Metrics {
	// New runtime records distinguish upstream attempts from the final
	// client-request summary. Records without OutcomeKnown are legacy/plugin
	// events and retain the historical one-record-one-request behaviour.
	external := record.ExternalRequest || !record.OutcomeKnown
	legacy := !record.ExternalRequest && !record.OutcomeKnown
	upstream := !external && !record.Supplemental
	tokens := TokenTotals{}
	costMicros := int64(0)
	var unpricedTokens int64
	unpricedAttempt := false
	if !record.ExternalRequest {
		tokens = reportedTokenTotals(record.Provider, record.Detail)
		billable := normalizeBillableTokens(record.Provider, record.Detail)
		costMicros, unpricedTokens, unpricedAttempt = pricing.calculate(record.Provider, record.Model, record.ServiceTier, billable)
	}
	version := ""
	if pricing != nil {
		version = pricing.version
	}
	var pricingVersions map[string]int64
	if version != "" && !record.ExternalRequest {
		pricingVersions = map[string]int64{version: 1}
	}
	var costsByCurrency map[string]int64
	currency := normalizedCurrency(pricingCurrency(pricing))
	if !record.ExternalRequest && (!unpricedAttempt || costMicros > 0) && currency != "UNKNOWN" {
		costsByCurrency = map[string]int64{currency: costMicros}
	}
	metrics := Metrics{
		Tokens:                        tokens,
		EstimatedCostMicros:           costMicros,
		EstimatedCostMicrosByCurrency: costsByCurrency,
		UnpricedTokens:                unpricedTokens,
		PricingVersions:               pricingVersions,
	}
	if external {
		metrics.Attempts = 1
		if record.Failed {
			metrics.Failed = 1
		} else {
			metrics.Success = 1
		}
	} else if upstream {
		metrics.UpstreamAttempts = 1
		if record.Failed {
			metrics.UpstreamFailedAttempts = 1
		}
	}
	if latencyMs := nonNegative(record.Latency.Milliseconds()); latencyMs > 0 && (external || legacy) {
		metrics.LatencyMsTotal = latencyMs
		metrics.LatencySamples = 1
	}
	if ttftMs := nonNegative(record.TTFT.Milliseconds()); ttftMs > 0 && (external || legacy) {
		metrics.TTFTMsTotal = ttftMs
		metrics.TTFTSamples = 1
	}
	if unpricedAttempt && (upstream || legacy) {
		metrics.UnpricedAttempts = 1
	}
	return metrics
}

// repriceAllLocked replaces pricing-only fields without changing usage,
// latency, success, or failure aggregates. The caller must hold c.mu.
func (c *Collector) repriceAllLocked() bool {
	changed := false
	for _, bucket := range c.buckets {
		c.repriceMetricsLocked(&bucket.Metrics, bucket.ClientKeyID, bucket.Provider, bucket.Model, bucket.ServiceTier)
		changed = true
	}
	for _, bucket := range c.recentBuckets {
		c.repriceMetricsLocked(&bucket.Metrics, bucket.ClientKeyID, bucket.Provider, bucket.Model, bucket.ServiceTier)
		changed = true
	}
	for _, metrics := range c.overflow {
		c.markOverflowUnpricedLocked(metrics)
		changed = true
	}
	return changed
}

func (c *Collector) repriceMetricsLocked(metrics *Metrics, clientKeyID, provider, model, serviceTier string) {
	if metrics == nil {
		return
	}
	detail := coreusage.Detail{
		InputTokens:         metrics.Tokens.InputTokens,
		OutputTokens:        metrics.Tokens.OutputTokens,
		ReasoningTokens:     metrics.Tokens.ReasoningTokens,
		CachedTokens:        metrics.Tokens.CachedTokens,
		CacheReadTokens:     metrics.Tokens.CacheReadTokens,
		CacheCreationTokens: metrics.Tokens.CacheCreationTokens,
		TotalTokens:         metrics.Tokens.TotalTokens,
	}
	billable := normalizeBillableTokens(provider, detail)
	pricing := c.pricingForClientLocked(clientKeyID)
	costMicros, unpricedTokens, unpricedAttempt := pricing.calculate(provider, model, serviceTier, billable)
	metrics.EstimatedCostMicros = costMicros
	metrics.EstimatedCostMicrosByCurrency = nil
	currency := normalizedCurrency(pricingCurrency(pricing))
	if (!unpricedAttempt || costMicros > 0) && currency != "UNKNOWN" {
		metrics.EstimatedCostMicrosByCurrency = map[string]int64{currency: costMicros}
	}
	metrics.UnpricedTokens = unpricedTokens
	metrics.UnpricedAttempts = 0
	if unpricedAttempt {
		metrics.UnpricedAttempts = pricingAttemptCount(*metrics)
	}
	c.setCurrentPricingVersionLocked(metrics, pricing)
}

func (c *Collector) markOverflowUnpricedLocked(metrics *Metrics) {
	if metrics == nil {
		return
	}
	metrics.EstimatedCostMicros = 0
	metrics.EstimatedCostMicrosByCurrency = nil
	metrics.UnpricedTokens = nonNegative(metrics.Tokens.TotalTokens)
	if metrics.UnpricedTokens == 0 {
		detail := coreusage.Detail{
			InputTokens:         metrics.Tokens.InputTokens,
			OutputTokens:        metrics.Tokens.OutputTokens,
			ReasoningTokens:     metrics.Tokens.ReasoningTokens,
			CachedTokens:        metrics.Tokens.CachedTokens,
			CacheReadTokens:     metrics.Tokens.CacheReadTokens,
			CacheCreationTokens: metrics.Tokens.CacheCreationTokens,
		}
		metrics.UnpricedTokens = reportedTokenTotals("", detail).TotalTokens
	}
	metrics.UnpricedAttempts = pricingAttemptCount(*metrics)
	c.setCurrentPricingVersionLocked(metrics, c.pricing)
}

func (c *Collector) setCurrentPricingVersionLocked(metrics *Metrics, pricing *pricingTable) {
	metrics.PricingVersions = nil
	pricingAttempts := pricingAttemptCount(*metrics)
	if pricing != nil && pricing.version != "" && pricingAttempts > 0 {
		metrics.PricingVersions = map[string]int64{pricing.version: pricingAttempts}
	}
}

func pricingAttemptCount(metrics Metrics) int64 {
	if metrics.UpstreamAttempts > 0 {
		return nonNegative(metrics.UpstreamAttempts)
	}
	// Snapshots created before the request/attempt split only have Attempts.
	// Use that value when token usage proves this is a legacy priced aggregate;
	// external-request summaries intentionally carry no tokens.
	if metrics.Tokens.TotalTokens > 0 || metrics.Tokens.InputTokens > 0 || metrics.Tokens.OutputTokens > 0 {
		return nonNegative(metrics.Attempts)
	}
	return 0
}

func pricingCurrency(pricing *pricingTable) string {
	if pricing == nil {
		return ""
	}
	return pricing.currency
}

func reportedTokenTotals(provider string, detail coreusage.Detail) TokenTotals {
	tokens := TokenTotals{
		InputTokens:         nonNegative(detail.InputTokens),
		OutputTokens:        nonNegative(detail.OutputTokens),
		ReasoningTokens:     nonNegative(detail.ReasoningTokens),
		CachedTokens:        nonNegative(detail.CachedTokens),
		CacheReadTokens:     nonNegative(detail.CacheReadTokens),
		CacheCreationTokens: nonNegative(detail.CacheCreationTokens),
		TotalTokens:         nonNegative(detail.TotalTokens),
	}
	if tokens.TotalTokens != 0 {
		return tokens
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case "claude":
		tokens.TotalTokens = saturatingAdd(tokens.InputTokens, tokens.OutputTokens)
		tokens.TotalTokens = saturatingAdd(tokens.TotalTokens, tokens.CacheReadTokens)
		tokens.TotalTokens = saturatingAdd(tokens.TotalTokens, tokens.CacheCreationTokens)
	case "gemini", "aistudio", "antigravity", "vertex":
		tokens.TotalTokens = saturatingAdd(tokens.InputTokens, tokens.OutputTokens)
		tokens.TotalTokens = saturatingAdd(tokens.TotalTokens, tokens.ReasoningTokens)
	default:
		tokens.TotalTokens = saturatingAdd(tokens.InputTokens, tokens.OutputTokens)
	}
	return tokens
}

func clientIdentity(record coreusage.Record) (id, alias, maskedKey string) {
	id = strings.TrimSpace(record.ClientKeyID)
	alias = strings.TrimSpace(record.ClientKeyAlias)
	rawKey := strings.TrimSpace(record.APIKey)
	if rawKey != "" {
		if strings.Contains(id, rawKey) {
			id = sdkaccess.FallbackClientKeyID(rawKey)
		}
		if strings.Contains(alias, rawKey) {
			alias = ""
		}
	}
	if id == "" && rawKey != "" {
		id = sdkaccess.FallbackClientKeyID(rawKey)
	}
	id = sanitizeIdentifier(id, anonymousClientKeyID)
	alias = sanitizeDisplayValue(alias, 128)
	if maskedKey == "" {
		maskedKey = maskAPIKey(rawKey)
	}
	maskedKey = sanitizeDisplayValue(maskedKey, 64)
	return id, alias, maskedKey
}

type clientKeyDisplay struct {
	alias     string
	maskedKey string
}

func configuredClientKeyDisplays(cfg *config.Config) (map[string]clientKeyDisplay, *strings.Replacer) {
	if cfg == nil || len(cfg.APIKeys) == 0 {
		return nil, nil
	}
	rawKeys := make([]string, 0, len(cfg.APIKeys))
	rawKeySet := make(map[string]struct{}, len(cfg.APIKeys))
	for _, rawKey := range cfg.APIKeys {
		apiKey := strings.TrimSpace(rawKey)
		if apiKey == "" {
			continue
		}
		if _, exists := rawKeySet[apiKey]; exists {
			continue
		}
		rawKeySet[apiKey] = struct{}{}
		rawKeys = append(rawKeys, apiKey)
	}
	replacements := make([]string, 0, len(rawKeys)*2)
	for _, rawKey := range rawKeys {
		replacements = append(replacements, rawKey, "")
	}
	redactor := strings.NewReplacer(replacements...)
	displays := make(map[string]clientKeyDisplay, len(rawKeys))
	for _, apiKey := range rawKeys {
		metadata := cfg.APIKeyMetadata[apiKey]
		id := strings.TrimSpace(metadata.ID)
		_, idIsRawKey := rawKeySet[id]
		if id == "" || idIsRawKey {
			id = sdkaccess.FallbackClientKeyID(apiKey)
		}
		id = sanitizeIdentifier(id, "")
		if id == "" {
			continue
		}
		if _, exists := displays[id]; exists {
			// Sanitized configurations have unique IDs. Ignore ambiguous entries
			// defensively instead of attaching an alias to the wrong history.
			continue
		}
		alias := strings.TrimSpace(metadata.Alias)
		if alias != "" && redactor.Replace(alias) != alias {
			alias = ""
		}
		display := clientKeyDisplay{
			alias:     sanitizeDisplayValue(alias, 128),
			maskedKey: sanitizeDisplayValue(maskAPIKey(apiKey), 64),
		}
		displays[id] = display
		fallbackID := sdkaccess.FallbackClientKeyID(apiKey)
		if _, exists := displays[fallbackID]; !exists {
			displays[fallbackID] = display
		}
	}
	return displays, redactor
}

func (c *Collector) sanitizeConfiguredSecretsLocked(id, alias string) (string, string) {
	if c == nil || c.secretRedactor == nil {
		return id, alias
	}
	if id != "" && c.secretRedactor.Replace(id) != id {
		id = sdkaccess.FallbackClientKeyID(id)
	}
	if alias != "" && c.secretRedactor.Replace(alias) != alias {
		alias = ""
	}
	return id, alias
}

func (c *Collector) safeHashedLabelLocked(value string) string {
	candidate := sdkaccess.FallbackClientKeyID(value)
	for attempts := 0; attempts < 4 && candidate != ""; attempts++ {
		if c == nil || c.secretRedactor == nil || c.secretRedactor.Replace(candidate) == candidate {
			return candidate
		}
		candidate = sdkaccess.FallbackClientKeyID(candidate)
	}
	return ""
}

func (c *Collector) safeCurrencyLabelLocked(value string) string {
	candidate := value
	for attempts := 0; attempts < 4; attempts++ {
		candidate = normalizedCurrency(c.safeHashedLabelLocked(candidate))
		if candidate == "UNKNOWN" || candidate == "" {
			return ""
		}
		if c == nil || c.secretRedactor == nil || c.secretRedactor.Replace(candidate) == candidate {
			return candidate
		}
	}
	return ""
}

func (c *Collector) sanitizePricingSecretsLocked(input config.UsagePricingConfig) {
	if c == nil || c.pricing == nil || c.secretRedactor == nil {
		return
	}
	currencyValue := strings.TrimSpace(input.Currency)
	if currencyValue == "" {
		currencyValue = c.pricing.currency
	}
	if c.secretRedactor.Replace(currencyValue) != currencyValue || c.secretRedactor.Replace(c.pricing.currency) != c.pricing.currency {
		c.pricing.currency = c.safeCurrencyLabelLocked(currencyValue)
	}
	versionValue := strings.TrimSpace(input.Version)
	if versionValue == "" {
		versionValue = c.pricing.version
	}
	if c.secretRedactor.Replace(versionValue) != versionValue || c.secretRedactor.Replace(c.pricing.version) != c.pricing.version {
		c.pricing.version = c.safeHashedLabelLocked(versionValue)
	}
}

func (c *Collector) sanitizeMetricsSecretsLocked(metrics *Metrics) bool {
	if c == nil || metrics == nil || c.secretRedactor == nil {
		return false
	}
	changed := false
	costs := make(map[string]int64, len(metrics.EstimatedCostMicrosByCurrency))
	for _, currency := range sortedCurrencyKeys(metrics.EstimatedCostMicrosByCurrency) {
		safeCurrency := normalizedCurrency(currency)
		if c.secretRedactor.Replace(currency) != currency || c.secretRedactor.Replace(safeCurrency) != safeCurrency {
			safeCurrency = c.safeCurrencyLabelLocked(currency)
			changed = true
		}
		if safeCurrency != "" {
			addCurrencyCost(costs, safeCurrency, metrics.EstimatedCostMicrosByCurrency[currency])
		}
	}
	metrics.EstimatedCostMicrosByCurrency = costs
	metrics.EstimatedCostMicros = singleCurrencyCost(costs)
	versions := make(map[string]int64, len(metrics.PricingVersions))
	for _, version := range sortedPricingVersions(metrics.PricingVersions) {
		safeVersion := version
		if c.secretRedactor.Replace(version) != version {
			safeVersion = c.safeHashedLabelLocked(version)
			changed = true
		}
		if safeVersion != "" {
			mergeVersionCounts(&versions, map[string]int64{safeVersion: metrics.PricingVersions[version]})
		}
	}
	metrics.PricingVersions = versions
	return changed
}

func (c *Collector) sanitizePersistedDimensionLocked(value, recordRawKey, fallback string) string {
	value = sanitizeDisplayValue(value, 256)
	recordRawKey = strings.TrimSpace(recordRawKey)
	if value == "" {
		return fallback
	}
	if recordRawKey != "" && strings.Contains(value, recordRawKey) {
		return fallback
	}
	if c != nil && c.secretRedactor != nil && c.secretRedactor.Replace(value) != value {
		return fallback
	}
	return value
}

func (c *Collector) sanitizeStoredClientMetadataLocked() bool {
	if c == nil || c.secretRedactor == nil {
		return false
	}
	changed := false
	rebuiltBuckets := make(map[bucketKey]*Bucket, len(c.buckets))
	for _, bucket := range c.buckets {
		id, _ := c.sanitizeConfiguredSecretsLocked(bucket.ClientKeyID, "")
		provider := c.sanitizePersistedDimensionLocked(bucket.Provider, "", unknownDimension)
		model := c.sanitizePersistedDimensionLocked(bucket.Model, "", unknownDimension)
		serviceTier := c.sanitizePersistedDimensionLocked(bucket.ServiceTier, "", coreusage.DefaultServiceTier)
		if id != bucket.ClientKeyID || provider != bucket.Provider || model != bucket.Model || serviceTier != bucket.ServiceTier {
			changed = true
		}
		copyBucket := *bucket
		copyBucket.ClientKeyID = id
		copyBucket.Provider = provider
		copyBucket.Model = model
		copyBucket.ServiceTier = serviceTier
		copyBucket.Metrics = cloneMetrics(bucket.Metrics)
		if c.sanitizeMetricsSecretsLocked(&copyBucket.Metrics) {
			changed = true
		}
		key := keyForBucket(copyBucket)
		if existing := rebuiltBuckets[key]; existing != nil {
			mergeMetrics(&existing.Metrics, copyBucket.Metrics)
			if existing.FirstUsedAt.IsZero() || (!copyBucket.FirstUsedAt.IsZero() && copyBucket.FirstUsedAt.Before(existing.FirstUsedAt)) {
				existing.FirstUsedAt = copyBucket.FirstUsedAt
			}
			if copyBucket.LastUsedAt.After(existing.LastUsedAt) {
				existing.LastUsedAt = copyBucket.LastUsedAt
			}
			changed = true
			continue
		}
		rebuiltBuckets[key] = &copyBucket
	}
	c.buckets = rebuiltBuckets

	rebuiltInfo := make(map[string]*ClientKeyInfo, len(c.clientKeys))
	for _, info := range c.clientKeys {
		id, alias := c.sanitizeConfiguredSecretsLocked(info.ID, info.Alias)
		if id != info.ID || alias != info.Alias {
			changed = true
		}
		copyInfo := *info
		copyInfo.ID = id
		copyInfo.Alias = alias
		if c.secretRedactor.Replace(copyInfo.MaskedKey) != copyInfo.MaskedKey {
			copyInfo.MaskedKey = ""
			changed = true
		}
		if existing := rebuiltInfo[id]; existing != nil {
			if existing.FirstUsedAt.IsZero() || (!copyInfo.FirstUsedAt.IsZero() && copyInfo.FirstUsedAt.Before(existing.FirstUsedAt)) {
				existing.FirstUsedAt = copyInfo.FirstUsedAt
			}
			if copyInfo.LastUsedAt.After(existing.LastUsedAt) {
				existing.LastUsedAt = copyInfo.LastUsedAt
			}
			changed = true
			continue
		}
		rebuiltInfo[id] = &copyInfo
	}
	c.clientKeys = rebuiltInfo
	return changed
}

func (c *Collector) refreshClientKeyMetadataLocked() bool {
	if c == nil || len(c.configuredKeys) == 0 {
		return false
	}
	changed := false
	for id, display := range c.configuredKeys {
		info := c.clientKeys[id]
		if info == nil {
			continue
		}
		if info.Alias != display.alias {
			info.Alias = display.alias
			changed = true
		}
		if info.MaskedKey != display.maskedKey {
			info.MaskedKey = display.maskedKey
			changed = true
		}
	}
	return changed
}

func maskAPIKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= 8 {
		return strings.Repeat("*", len(runes))
	}
	return string(runes[:4]) + "..." + string(runes[len(runes)-4:])
}

func sanitizeIdentifier(value, fallback string) string {
	value = stripControlRunes(strings.TrimSpace(value))
	if value == "" {
		return fallback
	}
	if len(value) <= 128 {
		return value
	}
	return sdkaccess.FallbackClientKeyID(value)
}

func sanitizeDisplayValue(value string, maxLen int) string {
	value = stripControlRunes(strings.TrimSpace(value))
	runes := []rune(value)
	if maxLen > 0 && len(runes) > maxLen {
		return string(runes[:maxLen])
	}
	return value
}

func stripControlRunes(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
}

func normalizedDimension(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return unknownDimension
	}
	runes := []rune(value)
	if len(runes) > 256 {
		return string(runes[:256])
	}
	return value
}

// PersistenceError returns the latest snapshot load or save error. A load error
// remains set because automatic replacement is blocked; a transient save error
// is cleared after the next successful flush.
func (c *Collector) PersistenceError() error {
	if c == nil {
		return nil
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	return c.persistenceErr
}
