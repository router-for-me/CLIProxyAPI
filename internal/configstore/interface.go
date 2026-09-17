// Package configstore defines the control-plane contract for reading and
// writing the canonical NixLLM runtime configuration. Phase 1 ships a
// PostgreSQL-backed implementation in the pg subpackage; future phases may
// add git- or object-store backends behind the same Repository interface.
//
// Callers funnel through the interface so handlers, CLI commands, and tests
// can substitute an in-memory stub without coupling to the persistence
// layer. The only typed error that crosses the interface is
// RevisionConflictError, which carries the current revision so handlers can
// map it to HTTP 409 Conflict.
package configstore

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/configsnapshot"
)

// SaveAudit captures who initiated a write. Actor and Reason populate the
// config_revisions audit columns. Source is optional; when empty the
// implementation defaults to "dashboard" (matching the existing CLI
// conventions). Callers that want the rollback path to record its own
// channel pass Source: "rollback" explicitly.
//
// The bootstrap path of the pg implementation (expected==0) additionally
// records one config_imports audit row; ImportSource and Mode populate
// that row's source_label and mode columns respectively. Both are optional
// and only meaningful on the bootstrap path.
type SaveAudit struct {
	Actor  string
	Reason string
	Source string
	// ImportSource is the on-disk YAML path (or other human label) the
	// bootstrap import originated from. Empty leaves source_label NULL.
	ImportSource string
	// Mode names the calling CLI surface (e.g. "import-config",
	// "boot-auto-import"). Empty defaults to "dashboard" so non-import
	// saves do not mislabel the audit trail.
	Mode string
}

// Repository is the control-plane interface for reading and writing the
// active runtime configuration. All operations are transactional: either the
// snapshot is fully persisted with a new revision row in config_revisions
// or no observable change is made.
//
// The interface returns *configsnapshot.Snapshot values (rather than the
// struct value) so callers can compare revisions by pointer and the
// Snapshot stays assignable to interface return types without copying.
type Repository interface {
	// Load returns the active runtime configuration. When the singleton is
	// absent (no accepted save has ever been recorded) Load returns a
	// NewEmpty snapshot with Revision 0 and a nil error so they can build
	// an initial candidate without special-casing the empty case.
	Load(ctx context.Context) (*configsnapshot.Snapshot, error)

	// Save writes candidate to the active singleton when the current
	// revision matches expected. It increments the revision by one and
	// appends a config_revisions row in the same transaction. When the
	// current revision differs from expected the call returns a
	// *RevisionConflictError carrying the actual current revision; no row
	// is written. A Save with expected==0 is the bootstrap path: the call
	// succeeds only when no row currently exists.
	Save(ctx context.Context, expected int64, candidate *configsnapshot.Snapshot, audit SaveAudit) (*configsnapshot.Snapshot, error)

	// Rollback copies the settings stored in config_revisions at
	// targetRevision into a new active revision, incrementing the active
	// revision counter by one and writing updated_source='rollback'. The
	// resulting snapshot is returned. When targetRevision does not exist
	// the call returns a descriptive error and no row is written.
	Rollback(ctx context.Context, targetRevision int64, reason string, actor string) (*configsnapshot.Snapshot, error)
}

// RevisionConflictError is returned by Save when the supplied expected
// revision does not match the current revision stored in runtime_config.
// Handlers map this to HTTP 409 Conflict in future phases; for now the
// control plane surfaces it directly to CLI callers.
//
// The error intentionally carries only the current revision. Callers that
// need to display the entire active snapshot should re-Load after
// receiving the conflict.
type RevisionConflictError struct {
	Current int64
}

// Error renders the typed error as a stable, package-prefixed string so log
// scrapers can route it without unwrapping.
func (e *RevisionConflictError) Error() string {
	return fmt.Sprintf("configstore: revision conflict; current is %d", e.Current)
}
