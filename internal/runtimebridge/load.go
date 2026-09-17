package runtimebridge

import (
	"context"
	"errors"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/configvalidation"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store/runtimeconfig"
)

// LoadConfigFromSnapshot loads the active runtime_config snapshot via the
// supplied store handle, validates the rendered bytes through
// configvalidation, and returns the parsed *config.Config. It does NOT build
// a bridge directory; that is the caller's responsibility. Returns an error
// when the source is nil or the snapshot has no active revision.
//
// The signature accepts *runtimeconfig.Store directly so the package keeps
// its dependency-light stance (cmd/server/main.go already imports
// runtimebridge and store/runtimeconfig transitively).
func LoadConfigFromSnapshot(ctx context.Context, rc *runtimeconfig.Store) (*config.Config, error) {
	if rc == nil {
		return nil, fmt.Errorf("runtimebridge: snapshot source is nil")
	}
	snap, err := rc.LoadRuntimeConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("runtimebridge: load snapshot: %w", err)
	}
	if snap == nil {
		return nil, errors.New("runtimebridge: nil snapshot")
	}
	if snap.Revision == 0 {
		return nil, errors.New("runtimebridge: runtime_config has no active revision")
	}
	cfg, err := configvalidation.Validate(snap)
	if err != nil {
		return nil, fmt.Errorf("runtimebridge: validate snapshot: %w", err)
	}
	return cfg, nil
}
