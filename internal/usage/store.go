package usage

import (
	"context"
	"strings"
	"sync"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

// StatisticsStore abstracts usage statistics persistence.
type StatisticsStore interface {
	Record(ctx context.Context, record coreusage.Record) error
	Snapshot(ctx context.Context) (StatisticsSnapshot, error)
	Export(ctx context.Context) (StatisticsSnapshot, error)
	Import(ctx context.Context, snapshot StatisticsSnapshot) (MergeResult, error)
	Close() error
}

var (
	defaultStore = NewMemoryStatisticsStore(defaultRequestStatistics)
	activeStore  = &storeManager{store: defaultStore}
)

type storeManager struct {
	mu    sync.RWMutex
	store StatisticsStore
}

// GetStatisticsStore returns the currently configured usage statistics store.
func GetStatisticsStore() StatisticsStore {
	activeStore.mu.RLock()
	defer activeStore.mu.RUnlock()
	return activeStore
}

// SetStatisticsStore swaps the active usage statistics store.
func SetStatisticsStore(store StatisticsStore) {
	if store == nil {
		store = defaultStore
	}
	activeStore.mu.Lock()
	previous := activeStore.store
	activeStore.store = store
	activeStore.mu.Unlock()
	if previous != nil && previous != store {
		_ = previous.Close()
	}
}

// ConfigureStatisticsStore creates and installs a store for the requested driver.
func ConfigureStatisticsStore(ctx context.Context, driver, databaseURL string, autoMigrate bool) error {
	switch normalizeUsageStoreDriver(driver) {
	case "memory":
		SetStatisticsStore(defaultStore)
		return nil
	case "postgres":
		store, err := NewPostgresStatisticsStore(ctx, databaseURL, autoMigrate)
		if err != nil {
			return err
		}
		SetStatisticsStore(store)
		return nil
	default:
		SetStatisticsStore(defaultStore)
		return nil
	}
}

func normalizeUsageStoreDriver(driver string) string {
	trimmed := strings.ToLower(strings.TrimSpace(driver))
	switch trimmed {
	case "", "memory":
		return "memory"
	case "postgres":
		return "postgres"
	default:
		return "memory"
	}
}

func (m *storeManager) Record(ctx context.Context, record coreusage.Record) error {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.store == nil {
		return nil
	}
	return m.store.Record(ctx, record)
}

func (m *storeManager) Snapshot(ctx context.Context) (StatisticsSnapshot, error) {
	if m == nil {
		return StatisticsSnapshot{}, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.store == nil {
		return StatisticsSnapshot{}, nil
	}
	return m.store.Snapshot(ctx)
}

func (m *storeManager) Export(ctx context.Context) (StatisticsSnapshot, error) {
	if m == nil {
		return StatisticsSnapshot{}, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.store == nil {
		return StatisticsSnapshot{}, nil
	}
	return m.store.Export(ctx)
}

func (m *storeManager) Import(ctx context.Context, snapshot StatisticsSnapshot) (MergeResult, error) {
	if m == nil {
		return MergeResult{}, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.store == nil {
		return MergeResult{}, nil
	}
	return m.store.Import(ctx, snapshot)
}

func (m *storeManager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	store := m.store
	m.store = defaultStore
	m.mu.Unlock()
	if store == nil || store == defaultStore {
		return nil
	}
	return store.Close()
}
