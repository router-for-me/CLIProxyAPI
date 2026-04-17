package mongostate

import (
	"context"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	log "github.com/sirupsen/logrus"
)

// Manager orchestrates periodic persistence and startup restoration of runtime state.
type Manager struct {
	store      *MongoStore
	cfg        StoreConfig
	cancel     context.CancelFunc
	mu         sync.Mutex
	flushSig   chan struct{}
	logLimiter int64 // Unix timestamp of last detailed error log
}

// NewManager creates a new state persistence manager.
func NewManager(store *MongoStore, cfg StoreConfig) *Manager {
	return &Manager{
		store:    store,
		cfg:      cfg,
		flushSig: make(chan struct{}, 1),
	}
}

// Restore loads persisted state from MongoDB and applies it to the in-memory registry and usage stats.
func (m *Manager) Restore(ctx context.Context) (restoredCB, restoredUsage bool, err error) {
	if m == nil || m.store == nil {
		return false, false, nil
	}

	doc, err := m.store.LoadState(ctx)
	if err != nil {
		log.Warnf("mongostate: restore: load failed, starting fresh: %v", err)
		return false, false, nil
	}
	if doc == nil {
		log.Debug("mongostate: restore: no persisted state found, starting fresh")
		return false, false, nil
	}

	if len(doc.CircuitBreakerSnapshot) > 0 {
		applied, skipped := registry.GetGlobalRegistry().RestoreCircuitBreakers(doc.CircuitBreakerSnapshot)
		log.Infof("mongostate: circuit breaker restored: applied=%d, skipped=%d", applied, skipped)
		restoredCB = applied > 0
	}

	if doc.SchemaVersion >= 1 && !isZeroSnapshot(doc.UsageSnapshot) {
		result := usage.GetRequestStatistics().MergeSnapshot(doc.UsageSnapshot)
		log.Infof("mongostate: usage restored: added=%d, skipped=%d", result.Added, result.Skipped)
		restoredUsage = result.Added > 0
	}

	return restoredCB, restoredUsage, nil
}

// FlushNow performs an immediate synchronous flush.
func (m *Manager) FlushNow(ctx context.Context) error {
	if m == nil {
		return nil
	}
	return m.flush(ctx)
}

// StartPeriodic begins a background goroutine that flushes state at the configured interval.
func (m *Manager) StartPeriodic(ctx context.Context, intervalSec int) {
	if m == nil {
		return
	}
	if intervalSec <= 0 {
		intervalSec = 30
	}
	interval := time.Duration(intervalSec) * time.Second

	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.mu.Unlock()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := m.flush(ctx); err != nil {
					m.logError("periodic flush failed", err)
				}
			case <-m.flushSig:
				if err := m.flush(ctx); err != nil {
					m.logError("signal flush failed", err)
				}
			}
		}
	}()

	log.Infof("mongostate: periodic flush started (interval=%v)", interval)
}

// Stop cancels the background flush goroutine.
func (m *Manager) Stop() {
	if m == nil {
		return
	}
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Close releases the underlying MongoDB client connection.
func (m *Manager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.Stop()
	m.mu.Lock()
	store := m.store
	m.store = nil
	m.mu.Unlock()
	if store == nil {
		return nil
	}
	return store.Close(ctx)
}

// flush captures current runtime state and persists it to MongoDB.
func (m *Manager) flush(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.store == nil {
		return nil
	}

	now := time.Now()
	usageSnapshot := usage.GetRequestStatistics().Snapshot()
	cbSnapshot := registry.GetGlobalRegistry().SnapshotCircuitBreakersPersist()

	doc := &RuntimeStateDoc{
		SchemaVersion:          SchemaVersion,
		UpdatedAt:              now,
		CircuitBreakerSnapshot: cbSnapshot,
		UsageSnapshot:          usageSnapshot,
	}

	if err := m.store.SaveState(ctx, doc); err != nil {
		return err
	}

	log.Debugf("mongostate: flushed at %s (cb_entries=%d, usage_total=%d)",
		now.Format(time.RFC3339), countCBEntries(cbSnapshot), usageSnapshot.TotalRequests)
	return nil
}

// logError rate-limits error logs to once per 60 seconds.
func (m *Manager) logError(msg string, err error) {
	const intervalSec = 60
	now := time.Now().Unix()
	if now-m.logLimiter < int64(intervalSec) {
		return
	}
	m.logLimiter = now
	log.Warnf("mongostate: %s: %v", msg, err)
}

func isZeroSnapshot(s usage.StatisticsSnapshot) bool {
	return s.TotalRequests == 0 && s.SuccessCount == 0 && s.FailureCount == 0
}

func countCBEntries(m map[string]map[string]registry.CircuitBreakerPersistStatus) int {
	if m == nil {
		return 0
	}
	cnt := 0
	for _, models := range m {
		if models != nil {
			cnt += len(models)
		}
	}
	return cnt
}
