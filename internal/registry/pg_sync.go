package registry

import (
	"context"
	"errors"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// ModelStoreOperator abstracts the persistence layer backing PGSync so the
// registry package does not need to import any store implementation (which
// would create an import cycle: store → sdk/cliproxy/auth → internal/config
// → internal/registry). The concrete adapter lives in the store package and
// implements this interface using registry.ModelInfo directly.
type ModelStoreOperator interface {
	// UpsertModels persists the supplied registry.ModelInfo values. The
	// operator is responsible for any internal type conversion.
	UpsertModels(ctx context.Context, models []*ModelInfo) error
	// SelectAllModels returns the full persisted catalog as ModelInfo values.
	SelectAllModels(ctx context.Context) ([]*ModelInfo, error)
	// DeleteModelsByProvider removes all rows for the given provider.
	DeleteModelsByProvider(ctx context.Context, provider string) (int64, error)
	// CountModels returns the number of rows in the catalog.
	CountModels(ctx context.Context) (int64, error)
}

// PGSync mirrors models_catalog updates between the in-memory ModelRegistry
// (and the remote catalog downloader) and PostgreSQL. It is registered as a
// ModelRegistryHook so dynamic client registrations are persisted, and also
// exposes UpsertModels / LoadAllModels helpers for the remote updater.
type PGSync struct {
	store ModelStoreOperator

	// hookMu prevents concurrent upserts for the same provider: registry
	// hooks can fire in bursts when many clients report models in parallel.
	hookMu sync.Mutex
}

// NewPGSync builds a PGSync bound to a ModelStoreOperator. Passing nil is
// allowed and turns PGSync into a no-op so callers can register it
// unconditionally without checking whether the PG backend is active.
func NewPGSync(store ModelStoreOperator) *PGSync {
	return &PGSync{store: store}
}

// Enabled reports whether the PG mirror is configured. Disabled when no store
// is wired in (e.g. PGSTORE_DSN not set).
func (s *PGSync) Enabled() bool {
	if s == nil {
		return false
	}
	return s.store != nil
}

// UpsertModels persists one or more registry.ModelInfo values to PG. The call
// is a no-op when PGSync is disabled, so it can be invoked unconditionally
// from the model updater's success path.
func (s *PGSync) UpsertModels(ctx context.Context, models []*ModelInfo) error {
	if !s.Enabled() || len(models) == 0 {
		return nil
	}
	// Deduplicate identical (id, provider) pairs from the same batch to
	// avoid inserting conflicting rows when the upstream catalog contains
	// aliases pointing at the same canonical model.
	seen := make(map[string]struct{}, len(models))
	deduped := make([]*ModelInfo, 0, len(models))
	for _, m := range models {
		if m == nil || m.ID == "" {
			continue
		}
		key := m.ID + "|" + OwnedByAsProvider(m)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, m)
	}
	if len(deduped) == 0 {
		return nil
	}
	if err := s.store.UpsertModels(ctx, deduped); err != nil {
		return err
	}
	return nil
}

// ReplaceProvider atomically swaps the persisted catalog for a given provider.
// It deletes the existing rows then upserts the supplied set. Callers should
// batch this in a transaction if they require strict atomicity; the common
// case (model updater refresh) tolerates a transient mix of old/new rows.
func (s *PGSync) ReplaceProvider(ctx context.Context, provider string, models []*ModelInfo) error {
	if !s.Enabled() {
		return nil
	}
	if _, err := s.store.DeleteModelsByProvider(ctx, provider); err != nil {
		return err
	}
	return s.UpsertModels(ctx, models)
}

// LoadAllModels returns the entire persisted catalog as registry.ModelInfo
// values. Used at startup to seed modelsCatalogStore when the PG backend is
// already populated, sparing the cold-start fetch when possible.
func (s *PGSync) LoadAllModels(ctx context.Context) ([]*ModelInfo, error) {
	if !s.Enabled() {
		return nil, errors.New("registry pg sync: not enabled")
	}
	return s.store.SelectAllModels(ctx)
}

// Count returns the number of persisted models (for bootstrap decisions).
func (s *PGSync) Count(ctx context.Context) (int64, error) {
	if !s.Enabled() {
		return 0, nil
	}
	return s.store.CountModels(ctx)
}

// OnModelsRegistered satisfies registry.ModelRegistryHook. It is invoked
// asynchronously by the registry when a client (provider) reports available
// models. The implementation upserts the batch with a short timeout so a
// stuck PG connection cannot stall the registry hook on the request path.
func (s *PGSync) OnModelsRegistered(ctx context.Context, provider, _ string, models []*ModelInfo) {
	if !s.Enabled() || len(models) == 0 {
		return
	}
	s.hookMu.Lock()
	defer s.hookMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.UpsertModels(ctx, models); err != nil {
		log.WithError(err).WithField("provider", provider).Debug("registry pg sync: OnModelsRegistered upsert failed")
	}
}

// OnModelsUnregistered satisfies registry.ModelRegistryHook. When a client
// disconnects we do not delete its models from PG: the catalog is a union of
// all potential upstream providers and survives client outages. Deletion only
// happens via ReplaceProvider from the model updater. This intentionally
// diverges from the in-memory ModelRegistry behavior.
func (s *PGSync) OnModelsUnregistered(_ context.Context, _, _ string) {
	// No-op: PG catalog outlives individual client sessions.
}

// Compile-time assertion that PGSync implements ModelRegistryHook.
var _ ModelRegistryHook = (*PGSync)(nil)

// OwnedByAsProvider derives a canonical provider key for a model. OwnedBy is
// preferred (it matches the upstream organization), falling back to Type
// when OwnedBy is empty (some embedded models omit it).
func OwnedByAsProvider(m *ModelInfo) string {
	if m == nil {
		return ""
	}
	if m.OwnedBy != "" {
		return m.OwnedBy
	}
	return m.Type
}

// EmbeddedModelsForSeed returns the full embedded model catalog as a flat
// []*ModelInfo slice. It is used by the server bootstrap to seed the
// models_catalog PostgreSQL table on first run. Each section (claude,
// gemini, codex-*, ...) is appended in order; cross-section models retain
// their original OwnedBy / Type fields so the PG upsert keys correctly.
func EmbeddedModelsForSeed() []*ModelInfo {
	return []*ModelInfo(appendMany(
		GetClaudeModels(),
		GetGeminiModels(),
		GetGeminiVertexModels(),
		GetAIStudioModels(),
		GetCodexFreeModels(),
		GetCodexTeamModels(),
		GetCodexPlusModels(),
		GetCodexProModels(),
		GetKimiModels(),
		GetAntigravityModels(),
		GetXAIModels(),
	))
}

// appendMany concatenates the supplied slices into a single slice. It is a
// helper kept local to avoid pulling in the slices package (which is only
// available in Go 1.21+; this package supports older toolchains via the
// sdk/access layer's compat shims).
func appendMany(slices ...[]*ModelInfo) []*ModelInfo {
	total := 0
	for _, s := range slices {
		total += len(s)
	}
	out := make([]*ModelInfo, 0, total)
	for _, s := range slices {
		out = append(out, s...)
	}
	return out
}
