// Package configsnapshot defines the canonical, in-memory representation of a
// NixLLM configuration snapshot and the helpers used to derive deterministic
// identifiers (e.g. SHA-256 checksums) from it.
//
// A Snapshot is the unit of revision that the PostgreSQL-first control plane
// stores, validates, projects to the ephemeral compatibility bridge, and
// surfaces through the dashboard. Snapshot has json: tags so the management
// HTTP layer can hand it to encoding/json without bespoke codecs; persistence
// as JSONB and exchange with import/export pipelines must still go through
// the custom canonical encoder (see Checksum), which is independent of the
// wire-format tags.
package configsnapshot

import (
	"bytes"
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"time"
)

// Snapshot is the canonical, immutable view of a NixLLM configuration
// revision. It is the value the repository returns to the runtime, the
// dashboard, and the import/export pipelines.
//
// Field semantics:
//
//   - Settings holds the normalized scalar/runtime configuration that today
//     lives at the top level of config.yaml (server binding, TLS, debug,
//     proxy, remote-management, plugins, etc.). It is modelled as
//     map[string]any so unknown or forward-compatible fields can survive
//     round-trips without bespoke structs.
//   - Extra holds forward-compatible fields that have not yet been promoted
//     to a dedicated section. They survive import/export verbatim and are
//     reported in dry-run diffs.
//   - ResourceRefs references normalized PostgreSQL tables (upstream
//     providers, proxy pools, model groups, etc.) by stable identity. They
//     are not duplicated into Settings; the snapshot is the projection
//     boundary that joins the singleton config row with normalized rows.
//   - Revision is the monotonically increasing active revision assigned by
//     the repository. It is metadata, never part of the canonical identity
//     of the snapshot contents.
//   - UpdatedAt is the database-side modification timestamp. Like Revision
//     it is metadata and must not influence the deterministic checksum.
//   - UpdatedBy identifies the actor (management token/user or
//     "system-import"). It is part of the canonical content so an import
//     performed by a different actor is recognized as a distinct change.
//   - UpdatedSource records the channel that produced the revision
//     ("dashboard", "cli-import", "rollback", "system"). It is part of the
//     canonical content.
type Snapshot struct {
	// Settings is the normalized scalar/runtime configuration. It is never
	// nil for a Snapshot returned by the repository; NewEmpty initializes
	// it to a non-nil empty map.
	Settings map[string]any `json:"settings"`

	// Extra holds forward-compatible fields that have not yet been promoted
	// to a dedicated section. It is never nil for a Snapshot returned by
	// the repository.
	Extra map[string]any `json:"extra"`

	// ResourceRefs references normalized PostgreSQL tables by stable
	// identity. Order is preserved.
	ResourceRefs []ResourceRef `json:"resource_refs"`

	// Revision is the active revision assigned by the repository. It is
	// excluded from the deterministic checksum.
	Revision int64 `json:"revision"`

	// UpdatedAt is the database-side modification timestamp. It is excluded
	// from the deterministic checksum.
	UpdatedAt time.Time `json:"updated_at"`

	// UpdatedBy identifies the actor that produced the revision.
	UpdatedBy string `json:"updated_by"`

	// UpdatedSource records the channel that produced the revision.
	UpdatedSource string `json:"updated_source"`
}

// ResourceRef references a row in a normalized PostgreSQL table from a
// Snapshot. It joins the canonical singleton JSONB row with the normalized
// domain tables without duplicating their content.
//
// Projection is the optional subset of the resource's fields that the
// snapshot needs to faithfully describe the resource for diff/import/export
// purposes (e.g. provider name and disabled flag). It is never authoritative
// for runtime behavior; the underlying row is.
type ResourceRef struct {
	// Kind names the normalized table (e.g. "upstream_provider",
	// "proxy_pool", "model_group").
	Kind string `json:"kind"`

	// ID is the primary key of the row.
	ID string `json:"id"`

	// StableKey is a human-stable identifier suitable for diff/import/export
	// (e.g. provider name). It may equal ID when no separate stable
	// identifier exists.
	StableKey string `json:"stable_key"`

	// Projection carries the snapshot-relevant fields. Order within slices
	// is preserved by Checksum; map keys are sorted.
	Projection map[string]any `json:"projection"`
}

// NewEmpty returns a Snapshot with non-nil empty maps, an empty
// ResourceRefs slice, Revision 0, and UpdatedSource "system". UpdatedAt is
// the zero time and UpdatedBy is the empty string.
//
// NewEmpty is the safe default for callers that build a Snapshot
// incrementally. Checksum on the returned value is deterministic and
// excludes the zero Revision/UpdatedAt.
func NewEmpty() Snapshot {
	return Snapshot{
		Settings:      map[string]any{},
		Extra:         map[string]any{},
		ResourceRefs:  []ResourceRef{},
		UpdatedSource: "system",
	}
}

// SnapshotFromConfig projects a parsed *config.Config into a bootstrap
// Snapshot for the initial import / boot auto-import paths.
//
// Settings receives the scalar runtime projection: the YAML top-level keys
// that appear in the scalarKeys allowlist. Every other top-level key lands
// in Extra, except the reserved "__extra" envelope which cannot occur in a
// fresh yaml.Marshal of *config.Config and is rejected defensively.
// ResourceRefs stays empty: normalized tables (upstream providers, client
// API keys, ...) are the source of truth for resources, mirroring the
// resource_snapshot='{}' convention the pg repository persists.
//
// Revision is 0 (the repository's bootstrap Save assigns revision 1) and
// UpdatedSource is "cli-import" so the audit trail reflects where this
// snapshot originated.
func SnapshotFromConfig(cfg *config.Config) (Snapshot, error) {
	if cfg == nil {
		return Snapshot{}, errors.New("configsnapshot: SnapshotFromConfig: cfg is nil")
	}
	empty := NewEmpty()
	empty.UpdatedSource = "cli-import"
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return Snapshot{}, fmt.Errorf("configsnapshot: marshal config for projection: %w", err)
	}
	var root map[string]any
	if err := yaml.NewDecoder(bytes.NewReader(raw)).Decode(&root); err != nil {
		if errors.Is(err, ErrEmptyYAML) {
			return empty, nil
		}
		return Snapshot{}, fmt.Errorf("configsnapshot: decode projected yaml: %w", err)
	}
	if root == nil {
		return empty, nil
	}
	for k, v := range root {
		nv, errNorm := normalizeYAMLValue(v)
		if errNorm != nil {
			return Snapshot{}, fmt.Errorf("configsnapshot: normalize %s: %w", k, errNorm)
		}
		if k == extraEnvelope {
			return Snapshot{}, fmt.Errorf("configsnapshot: %s cannot occur in a projected config; refusing to guess", extraEnvelope)
		}
		if _, known := scalarKeySet[k]; known {
			empty.Settings[k] = nv
		} else {
			empty.Extra[k] = nv
		}
	}
	return empty, nil
}
