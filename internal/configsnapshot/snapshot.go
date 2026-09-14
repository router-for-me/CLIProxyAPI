// Package configsnapshot defines the canonical, in-memory representation of a
// NixLLM configuration snapshot and the helpers used to derive deterministic
// identifiers (e.g. SHA-256 checksums) from it.
//
// A Snapshot is the unit of revision that the PostgreSQL-first control plane
// stores, validates, projects to the ephemeral compatibility bridge, and
// surfaces through the dashboard. The type is intentionally JSON-friendly so
// it can be persisted as a JSONB document and exchanged with import/export
// pipelines without further translation.
package configsnapshot

import "time"

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
	Settings map[string]any

	// Extra holds forward-compatible fields that have not yet been promoted
	// to a dedicated section. It is never nil for a Snapshot returned by
	// the repository.
	Extra map[string]any

	// ResourceRefs references normalized PostgreSQL tables by stable
	// identity. Order is preserved.
	ResourceRefs []ResourceRef

	// Revision is the active revision assigned by the repository. It is
	// excluded from the deterministic checksum.
	Revision int64

	// UpdatedAt is the database-side modification timestamp. It is excluded
	// from the deterministic checksum.
	UpdatedAt time.Time

	// UpdatedBy identifies the actor that produced the revision.
	UpdatedBy string

	// UpdatedSource records the channel that produced the revision.
	UpdatedSource string
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
	Kind string

	// ID is the primary key of the row.
	ID string

	// StableKey is a human-stable identifier suitable for diff/import/export
	// (e.g. provider name). It may equal ID when no separate stable
	// identifier exists.
	StableKey string

	// Projection carries the snapshot-relevant fields. Order within slices
	// is preserved by Checksum; map keys are sorted.
	Projection map[string]any
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
