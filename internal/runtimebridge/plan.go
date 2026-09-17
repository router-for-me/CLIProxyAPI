package runtimebridge

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/configsnapshot"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/configvalidation"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	runtimeconfigstore "github.com/router-for-me/CLIProxyAPI/v7/internal/store/runtimeconfig"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util/atomicfile"
)

// PlanFromConfig returns a SnapshotRenderer that re-reads the active
// runtime_config snapshot from PostgreSQL on every Coordinator.ReloadLatest
// call. The renderer wires the same MarshalYAML + Validate path used during
// boot, so a successful commit/rollback re-renders the bridge file in place
// without restarting the process.
//
// The pg handle may be nil (returns an error). ctx is honored only at the
// reload call site — this constructor performs no I/O of its own.
func PlanFromConfig(ctx context.Context, pg *store.PostgresStore) (SnapshotRenderer, error) {
	_ = ctx
	if pg == nil {
		return nil, errors.New("runtimebridge: PlanFromConfig: pg handle is nil")
	}
	rc := runtimeconfigstore.New(pg)
	return &configSnapshotPlan{store: rc}, nil
}

// configSnapshotPlan implements SnapshotRenderer by always pulling the
// latest snapshot from PG. MarshalYAML runs first (to surface a malformed
// snapshot as a marshal error), then Validate parses + validates the bytes
// to confirm the file we are about to write survives configvalidation.
type configSnapshotPlan struct {
	store *runtimeconfigstore.Store
}

// MarshalYAML loads the latest snapshot from PG and projects it to YAML
// using configsnapshot.MarshalYAML (the same projection used at boot).
func (p *configSnapshotPlan) MarshalYAML() ([]byte, error) {
	if p == nil || p.store == nil {
		return nil, errors.New("runtimebridge: nil plan store")
	}
	snap, err := p.store.LoadRuntimeConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("load runtime_config: %w", err)
	}
	if snap == nil {
		return nil, errors.New("runtimebridge: nil snapshot")
	}
	if snap.Revision == 0 {
		return nil, errors.New("runtimebridge: runtime_config has no active revision")
	}
	return configsnapshot.MarshalYAML(snap)
}

// Validate parses the snapshot bytes through configvalidation to confirm
// the file we are about to write parses back into a *config.Config without
// errors. The returned *config.Config is discarded; we only care that the
// validation pipeline accepts the bytes.
func (p *configSnapshotPlan) Validate() error {
	if p == nil || p.store == nil {
		return errors.New("runtimebridge: nil plan store")
	}
	snap, err := p.store.LoadRuntimeConfig(context.Background())
	if err != nil {
		return fmt.Errorf("load runtime_config: %w", err)
	}
	if snap == nil {
		return errors.New("runtimebridge: nil snapshot")
	}
	if snap.Revision == 0 {
		return errors.New("runtimebridge: runtime_config has no active revision")
	}
	if _, err := configvalidation.Validate(snap); err != nil {
		return fmt.Errorf("validate snapshot: %w", err)
	}
	return nil
}

// AtomicWrite delegates to the shared util/atomicfile helper. The bridge
// therefore never depends on a per-package atomic-write copy.
func (p *configSnapshotPlan) AtomicWrite(path string, data []byte, mode os.FileMode) error {
	return atomicfile.Write(path, data, mode)
}
