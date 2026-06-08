package usage

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const (
	defaultBufferSize      = 100
	defaultFlushInterval   = 5 * time.Second
	defaultRetentionDays   = 30
	defaultCleanupInterval = 4 * time.Hour
	finalCacheMaxEntries   = 20000
	finalCacheTTL          = time.Hour
)

var (
	databasePlugin   *DatabasePlugin
	databasePluginMu sync.RWMutex
)

type databasePluginAdapter struct{}

func (databasePluginAdapter) HandleUsage(ctx context.Context, record coreusage.Record) {
	plugin := GetDatabasePlugin()
	if plugin == nil {
		return
	}
	plugin.HandleUsage(ctx, record)
}

func (databasePluginAdapter) HandleRequestFinal(ctx context.Context, final coreusage.RequestFinal) {
	plugin := GetDatabasePlugin()
	if plugin == nil {
		return
	}
	plugin.HandleRequestFinal(ctx, final)
}

func init() {
	coreusage.RegisterPlugin(databasePluginAdapter{})
}

func NormalizeRetentionDays(retentionDays int) int {
	if retentionDays > 0 {
		return retentionDays
	}
	return defaultRetentionDays
}

// DatabasePlugin persists usage records to database and provides combined statistics.
type DatabasePlugin struct {
	store             UsageStore
	retentionDays     int
	storeOnlySnapshot bool

	// Write buffer
	buffer    []UsageRecord
	bufferMu  sync.Mutex
	flushCh   chan struct{}
	closeCh   chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup

	finalMu    sync.Mutex
	finalCache map[string]requestFinalState
}

type requestFinalState struct {
	success bool
	seenAt  time.Time
}

// InitDatabasePlugin initializes the global database plugin.
// If initialization fails, returns the error but does not prevent the system from running.
func InitDatabasePlugin(ctx context.Context, pgDSN, pgSchema, authDir string, retentionDays int) error {
	if ctx == nil {
		ctx = context.Background()
	}

	databasePluginMu.RLock()
	alreadyEnabled := databasePlugin != nil
	databasePluginMu.RUnlock()
	if alreadyEnabled {
		return nil
	}

	databasePluginMu.Lock()
	defer databasePluginMu.Unlock()
	if databasePlugin != nil {
		return nil
	}

	store, err := NewUsageStore(ctx, pgDSN, pgSchema, authDir)
	if err != nil {
		return err
	}

	retentionDays = NormalizeRetentionDays(retentionDays)

	plugin := &DatabasePlugin{
		store:             store,
		retentionDays:     retentionDays,
		storeOnlySnapshot: strings.TrimSpace(pgDSN) != "",
		buffer:            make([]UsageRecord, 0, defaultBufferSize),
		flushCh:           make(chan struct{}, 1),
		closeCh:           make(chan struct{}),
		finalCache:        make(map[string]requestFinalState),
	}
	plugin.wg.Add(1)
	go plugin.flushLoop()

	// Run initial cleanup of old records
	if deleted, cleanupErr := store.DeleteOldRecords(ctx, retentionDays); cleanupErr != nil {
		log.WithError(cleanupErr).Warn("usage: failed to cleanup old records on startup")
	} else if deleted > 0 {
		log.WithField("deleted", deleted).Infof("usage: cleaned up records older than %d days", retentionDays)
	}

	databasePlugin = plugin
	return nil
}

// GetDatabasePlugin returns the global database plugin instance, or nil if not initialized.
func GetDatabasePlugin() *DatabasePlugin {
	databasePluginMu.RLock()
	defer databasePluginMu.RUnlock()
	return databasePlugin
}

// DatabaseStoreOnlySnapshotEnabled reports whether database usage statistics are
// the authoritative snapshot source. In this mode the in-memory logger should
// avoid duplicating per-request details that are already persisted by the
// database plugin.
func DatabaseStoreOnlySnapshotEnabled() bool {
	databasePluginMu.RLock()
	defer databasePluginMu.RUnlock()
	return databasePlugin != nil && databasePlugin.storeOnlySnapshot
}

// CloseDatabasePlugin closes the database connection after flushing pending writes.
func CloseDatabasePlugin() {
	databasePluginMu.Lock()
	plugin := databasePlugin
	databasePlugin = nil
	databasePluginMu.Unlock()

	if plugin == nil {
		return
	}
	plugin.closeOnce.Do(func() {
		close(plugin.closeCh)
	})
	plugin.wg.Wait()
	if plugin.store != nil {
		_ = plugin.store.Close()
	}
}

func (p *DatabasePlugin) SetRetentionDays(retentionDays int) {
	if p == nil {
		return
	}
	p.retentionDays = NormalizeRetentionDays(retentionDays)
}

func (p *DatabasePlugin) CleanupExpiredRecords(ctx context.Context) (int64, error) {
	if p == nil || p.store == nil {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return p.store.DeleteOldRecords(ctx, NormalizeRetentionDays(p.retentionDays))
}

// UpdatePersistence enables or disables usage persistence based on the provided flag.
// When enabling, it reads PGSTORE_DSN/pgstore_dsn and PGSTORE_SCHEMA/pgstore_schema
// from environment variables to determine the database backend.
// If PGSTORE_DSN is empty, SQLite is used with the database stored in authDir.
func UpdatePersistence(ctx context.Context, enabled bool, authDir string, retentionDays int) error {
	if !enabled {
		CloseDatabasePlugin()
		return nil
	}
	pgStoreDSN := util.GetEnvTrimmed("PGSTORE_DSN", "pgstore_dsn")
	pgStoreSchema := util.GetEnvTrimmed("PGSTORE_SCHEMA", "pgstore_schema")
	CloseDatabasePlugin()
	if pgStoreDSN == "" {
		return InitDatabasePlugin(ctx, "", "", authDir, retentionDays)
	}
	if err := InitDatabasePlugin(ctx, pgStoreDSN, pgStoreSchema, authDir, retentionDays); err != nil {
		log.WithError(err).Warn("usage: postgres unavailable, falling back to local usage storage")
		CloseDatabasePlugin()
		return InitDatabasePlugin(ctx, "", "", authDir, retentionDays)
	}
	return nil
}

// UsingSQLiteBackend returns true if the usage persistence would use SQLite (no PGSTORE_DSN set).
func UsingSQLiteBackend() bool {
	return util.GetEnvTrimmed("PGSTORE_DSN", "pgstore_dsn") == ""
}

// flushLoop periodically flushes the buffer to the database and cleans up old records.
func (p *DatabasePlugin) flushLoop() {
	defer p.wg.Done()
	flushTicker := time.NewTicker(defaultFlushInterval)
	cleanupTicker := time.NewTicker(defaultCleanupInterval)
	defer flushTicker.Stop()
	defer cleanupTicker.Stop()

	for {
		select {
		case <-p.closeCh:
			p.flush()
			return
		case <-flushTicker.C:
			p.flush()
		case <-cleanupTicker.C:
			p.cleanup()
		case <-p.flushCh:
			p.flush()
		}
	}
}

// cleanup deletes records older than the retention period.
func (p *DatabasePlugin) cleanup() {
	if p.store == nil || p.retentionDays <= 0 {
		return
	}
	deleted, err := p.store.DeleteOldRecords(context.Background(), p.retentionDays)
	if err != nil {
		log.WithError(err).Warn("usage: failed to cleanup old records")
	} else if deleted > 0 {
		log.WithField("deleted", deleted).Debugf("usage: cleaned up records older than %d days", p.retentionDays)
	}
}

// flush writes all buffered records to the database.
func (p *DatabasePlugin) flush() {
	p.bufferMu.Lock()
	if len(p.buffer) == 0 {
		p.bufferMu.Unlock()
		return
	}
	records := p.buffer
	p.buffer = make([]UsageRecord, 0, defaultBufferSize)
	p.bufferMu.Unlock()

	added, skipped, err := p.store.InsertBatch(context.Background(), records)
	if err != nil {
		log.WithError(err).Warn("usage: failed to flush records to database")
	} else if skipped > 0 {
		log.WithFields(log.Fields{"added": added, "skipped": skipped}).Debug("usage: flushed records")
	}
}

// triggerFlush signals the flush loop to flush immediately if buffer is full.
func (p *DatabasePlugin) triggerFlush() {
	select {
	case p.flushCh <- struct{}{}:
	default:
	}
}

// ginMethodPath extracts the HTTP method and path from a gin.Context stored in ctx.
func ginMethodPath(ctx context.Context) (string, string) {
	if ctx == nil {
		return "", ""
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil || ginCtx.Request == nil {
		return "", ""
	}
	return ginCtx.Request.Method, ginCtx.Request.URL.Path
}

// HandleUsage implements coreusage.Plugin interface.
// It buffers the usage record and flushes to database when buffer is full or on interval.
func (p *DatabasePlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil || p.store == nil {
		return
	}

	method, path := ginMethodPath(ctx)
	attempt := coreusage.RequestAttemptFromContext(ctx)
	requestID := strings.TrimSpace(record.RequestID)
	if requestID == "" {
		requestID = strings.TrimSpace(attempt.RequestID)
	}
	if requestID == "" {
		requestID = internallogging.GetRequestID(ctx)
	}
	attemptNo := record.AttemptNo
	if attemptNo <= 0 {
		attemptNo = attempt.AttemptNo
	}
	retryReason := strings.TrimSpace(record.RetryReason)
	if retryReason == "" {
		retryReason = attempt.RetryReason
	}
	finalSuccess := finalSuccessUnknown
	if record.FinalSuccess != nil {
		finalSuccess = boolToFinalSuccess(*record.FinalSuccess)
	} else if cached, ok := p.requestFinal(requestID); ok {
		finalSuccess = boolToFinalSuccess(cached)
	}

	dbRecord := UsageRecord{
		APIKey:             record.APIKey,
		Model:              record.Model,
		Source:             record.Source,
		AuthIndex:          record.AuthIndex,
		RequestID:          requestID,
		AttemptNo:          attemptNo,
		RetryReason:        retryReason,
		FinalSuccess:       finalSuccess,
		Failed:             record.Failed,
		RequestedAt:        record.RequestedAt,
		InputTokens:        record.Detail.InputTokens,
		OutputTokens:       record.Detail.OutputTokens,
		ReasoningTokens:    record.Detail.ReasoningTokens,
		CachedTokens:       record.Detail.CachedTokens,
		TotalTokens:        record.Detail.TotalTokens,
		Method:             method,
		Path:               path,
		ProviderStatusCode: record.ProviderStatusCode,
		ErrorCode:          record.ErrorCode,
	}

	p.bufferMu.Lock()
	p.buffer = append(p.buffer, dbRecord)
	shouldFlush := len(p.buffer) >= defaultBufferSize
	p.bufferMu.Unlock()

	if shouldFlush {
		p.triggerFlush()
	}
}

// HandleRequestFinal records the final outcome for all attempt rows sharing a request_id.
func (p *DatabasePlugin) HandleRequestFinal(ctx context.Context, final coreusage.RequestFinal) {
	if p == nil || p.store == nil {
		return
	}
	requestID := strings.TrimSpace(final.RequestID)
	if requestID == "" {
		return
	}
	p.rememberRequestFinal(requestID, final.FinalSuccess, final.CompletedAt)

	finalValue := boolToFinalSuccess(final.FinalSuccess)
	p.bufferMu.Lock()
	for i := range p.buffer {
		if p.buffer[i].RequestID == requestID {
			p.buffer[i].FinalSuccess = finalValue
		}
	}
	p.bufferMu.Unlock()

	p.triggerFlush()
	go func() {
		if err := p.store.UpdateRequestFinal(context.Background(), requestID, final.FinalSuccess); err != nil {
			log.WithError(err).WithField("request_id", requestID).Warn("usage: failed to update request final outcome")
		}
	}()
}

// HandleStreamSummary persists stream-level timing and completion metadata keyed by request_id + attempt_no.
func (p *DatabasePlugin) HandleStreamSummary(ctx context.Context, summary StreamSummaryRecord) {
	if p == nil || p.store == nil {
		return
	}

	attempt := coreusage.RequestAttemptFromContext(ctx)
	if summary.RequestID == "" {
		summary.RequestID = strings.TrimSpace(attempt.RequestID)
	}
	if summary.RequestID == "" {
		summary.RequestID = internallogging.GetRequestID(ctx)
	}
	if summary.AttemptNo <= 0 {
		summary.AttemptNo = attempt.AttemptNo
	}
	normalized, ok := normalizeStreamSummaryRecord(summary)
	if !ok {
		return
	}

	go func(record StreamSummaryRecord) {
		if err := p.store.UpsertStreamSummary(context.Background(), record); err != nil {
			log.WithError(err).WithFields(log.Fields{
				"request_id": record.RequestID,
				"attempt_no": record.AttemptNo,
			}).Warn("usage: failed to persist stream summary")
		}
	}(normalized)
}

func boolToFinalSuccess(success bool) int {
	if success {
		return finalSuccessTrue
	}
	return finalSuccessFalse
}

func finalSuccessValue(success *bool) int {
	if success == nil {
		return finalSuccessUnknown
	}
	return boolToFinalSuccess(*success)
}

func (p *DatabasePlugin) requestFinal(requestID string) (bool, bool) {
	if p == nil {
		return false, false
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return false, false
	}
	p.finalMu.Lock()
	defer p.finalMu.Unlock()
	state, ok := p.finalCache[requestID]
	if !ok {
		return false, false
	}
	if time.Since(state.seenAt) > finalCacheTTL {
		delete(p.finalCache, requestID)
		return false, false
	}
	return state.success, true
}

func (p *DatabasePlugin) rememberRequestFinal(requestID string, success bool, seenAt time.Time) {
	if p == nil {
		return
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}
	if seenAt.IsZero() {
		seenAt = time.Now()
	}
	p.finalMu.Lock()
	defer p.finalMu.Unlock()
	if p.finalCache == nil {
		p.finalCache = make(map[string]requestFinalState)
	}
	p.finalCache[requestID] = requestFinalState{success: success, seenAt: seenAt}
	if len(p.finalCache) <= finalCacheMaxEntries {
		return
	}
	cutoff := time.Now().Add(-finalCacheTTL)
	for id, state := range p.finalCache {
		if state.seenAt.Before(cutoff) {
			delete(p.finalCache, id)
		}
	}
	for len(p.finalCache) > finalCacheMaxEntries/2 {
		for id := range p.finalCache {
			delete(p.finalCache, id)
			break
		}
	}
}

// GetCombinedSnapshot returns combined statistics from database history and current session memory.
func (p *DatabasePlugin) GetCombinedSnapshot() StatisticsSnapshot {
	memStats := GetRequestStatistics().Snapshot()
	if p == nil || p.store == nil {
		return memStats
	}

	dbStats, err := p.store.GetAggregatedStats(context.Background())
	if err != nil {
		log.WithError(err).Warn("usage: failed to query database stats, returning memory only")
		return memStats
	}

	if p.storeOnlySnapshot {
		return mergeStats(dbStats, StatisticsSnapshot{})
	}
	return mergeStats(dbStats, memStats)
}

// ImportRecords imports usage records from a snapshot into the database.
// Returns the number of records added and skipped.
func (p *DatabasePlugin) ImportRecords(snapshot StatisticsSnapshot) (added, skipped int64, err error) {
	if p == nil || p.store == nil {
		return 0, 0, nil
	}

	var records []UsageRecord
	for apiKey, apiSnapshot := range snapshot.APIs {
		for model, modelSnapshot := range apiSnapshot.Models {
			for _, detail := range modelSnapshot.Details {
				records = append(records, UsageRecord{
					APIKey:             apiKey,
					Model:              model,
					Source:             detail.Source,
					AuthIndex:          detail.AuthIndex,
					RequestID:          detail.RequestID,
					AttemptNo:          detail.AttemptNo,
					RetryReason:        detail.RetryReason,
					FinalSuccess:       finalSuccessValue(detail.FinalSuccess),
					Failed:             detail.Failed,
					RequestedAt:        detail.Timestamp,
					InputTokens:        detail.Tokens.InputTokens,
					OutputTokens:       detail.Tokens.OutputTokens,
					ReasoningTokens:    detail.Tokens.ReasoningTokens,
					CachedTokens:       detail.Tokens.CachedTokens,
					TotalTokens:        detail.Tokens.TotalTokens,
					ProviderStatusCode: detail.ProviderStatusCode,
					ErrorCode:          detail.ErrorCode,
				})
			}
		}
	}

	if len(records) == 0 {
		return 0, 0, nil
	}

	return p.store.InsertBatch(context.Background(), records)
}

// mergeStats combines database aggregated stats with in-memory session stats.
func mergeStats(db AggregatedStats, mem StatisticsSnapshot) StatisticsSnapshot {
	result := StatisticsSnapshot{
		TotalRequests:  db.TotalRequests + mem.TotalRequests,
		SuccessCount:   db.SuccessCount + mem.SuccessCount,
		FailureCount:   db.FailureCount + mem.FailureCount,
		TotalTokens:    db.TotalTokens + mem.TotalTokens,
		APIs:           make(map[string]APISnapshot),
		RequestsByDay:  make(map[string]int64),
		RequestsByHour: make(map[string]int64),
		TokensByDay:    make(map[string]int64),
		TokensByHour:   make(map[string]int64),
	}

	// Merge APIs from database (aggregated counts only, no per-request details)
	for key, as := range db.APIs {
		apiSnap := APISnapshot{
			TotalRequests: as.TotalRequests,
			TotalTokens:   as.TotalTokens,
			Models:        make(map[string]ModelSnapshot),
		}
		for model, ms := range as.Models {
			apiSnap.Models[model] = ModelSnapshot{
				TotalRequests: ms.TotalRequests,
				TotalTokens:   ms.TotalTokens,
				Details:       []RequestDetail{},
			}
		}
		result.APIs[key] = apiSnap
	}

	// Merge APIs from memory (adds current session details)
	for key, as := range mem.APIs {
		existing, ok := result.APIs[key]
		if !ok {
			result.APIs[key] = as
		} else {
			existing.TotalRequests += as.TotalRequests
			existing.TotalTokens += as.TotalTokens
			for model, ms := range as.Models {
				if existingModel, modelOK := existing.Models[model]; modelOK {
					existingModel.TotalRequests += ms.TotalRequests
					existingModel.TotalTokens += ms.TotalTokens
					existingModel.Details = append(existingModel.Details, ms.Details...)
					existing.Models[model] = existingModel // FIX: Write back to map
				} else {
					existing.Models[model] = ms
				}
			}
			result.APIs[key] = existing
		}
	}

	// Merge time-based stats
	for k, v := range db.RequestsByDay {
		result.RequestsByDay[k] = v
	}
	for k, v := range mem.RequestsByDay {
		result.RequestsByDay[k] += v
	}

	for k, v := range db.RequestsByHour {
		result.RequestsByHour[k] = v
	}
	for k, v := range mem.RequestsByHour {
		result.RequestsByHour[k] += v
	}

	for k, v := range db.TokensByDay {
		result.TokensByDay[k] = v
	}
	for k, v := range mem.TokensByDay {
		result.TokensByDay[k] += v
	}

	for k, v := range db.TokensByHour {
		result.TokensByHour[k] = v
	}
	for k, v := range mem.TokensByHour {
		result.TokensByHour[k] += v
	}

	return result
}

// GetDetails returns paginated request details from the database.
func (p *DatabasePlugin) GetDetails(ctx context.Context, offset, limit int) ([]DetailRecord, error) {
	if p == nil || p.store == nil {
		return nil, nil
	}
	return p.store.GetDetails(ctx, offset, limit)
}
