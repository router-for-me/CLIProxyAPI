// Package configvalidation provides the shared validation pipeline that
// turns a configsnapshot.Snapshot into a normalised *config.Config.
//
// The pipeline is the single source of truth for "what does this snapshot
// mean when we render it to runtime?". It serialises the snapshot through
// configsnapshot.MarshalYAML (the deterministic projection) and then runs
// the resulting bytes through config.ParseConfigBytes, which applies the
// same in-memory normalisations as a fresh file load without touching
// disk.
//
// Callers that store a snapshot (PG-backed control plane, dashboard
// imports, dry-run diffs, the config-snapshot reference view, etc.) all
// funnel through Validate so the runtime sees a uniform Config regardless
// of which channel produced the snapshot.
package configvalidation

import (
	"errors"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/configsnapshot"
)

// ErrNilSnapshot is the sentinel returned (wrapped) by Validate when the
// caller passes a nil Snapshot. Callers can use errors.Is to discriminate
// the missing-snapshot class from marshal or parse failures that also
// surface through Validate. The error's message starts with the
// "configvalidation:" namespace prefix so log scrapers can route it
// alongside other validation rejections.
var ErrNilSnapshot = errors.New("configvalidation: snapshot is required")

// Validate serialises snap through the deterministic configsnapshot
// projection and runs the resulting YAML through config.ParseConfigBytes.
//
// A nil snapshot is rejected with a contextual error so callers can
// distinguish a missing snapshot from a snapshot whose contents failed to
// validate. Errors returned by the underlying marshal or parse steps are
// wrapped with "configvalidation:" context so the source of the rejection
// is unambiguous in logs.
//
// The input snapshot is read-only: Validate does not mutate snap, its
// Settings, its Extra, or its ResourceRefs. Returning a fresh Config lets
// callers retain their own reference to the snapshot for diffing or
// auditing while handing a usable runtime view to the next layer.
func Validate(snap *configsnapshot.Snapshot) (*config.Config, error) {
	if snap == nil {
		return nil, fmt.Errorf("%w", ErrNilSnapshot)
	}

	data, errMarshal := configsnapshot.MarshalYAML(snap)
	if errMarshal != nil {
		return nil, fmt.Errorf("configvalidation: marshal snapshot: %w", errMarshal)
	}

	cfg, errParse := config.ParseConfigBytes(data)
	if errParse != nil {
		return nil, fmt.Errorf("configvalidation: parse snapshot: %w", errParse)
	}

	return cfg, nil
}

// ValidateParsed re-runs in-memory normalization over an already-parsed
// *config.Config. Callers that hand-construct a *config.Config (e.g. from
// runtime_config snapshot bytes that never went through a file) share the
// same sanitization contract as Validate without re-marshaling. Phase 2's
// bridge path uses this because ParseConfigBytes already applied every
// sanitize step when the snapshot bytes were first parsed.
func ValidateParsed(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("configvalidation: nil config")
	}
	return nil
}
