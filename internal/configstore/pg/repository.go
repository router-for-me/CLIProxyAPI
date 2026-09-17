// Package pg implements the configstore.Repository contract against the
// PostgreSQL-backed control plane. It assumes the runtime_config,
// config_revisions, and (by extension) config_imports tables already exist
// in the configured schema; callers are expected to invoke
// store.PostgresStore.EnsureSchema before opening the repository so the
// schema is materialised. The repository never creates or migrates tables
// itself: that responsibility belongs to the store layer.
package pg

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/configsnapshot"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/configstore"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// Open returns a Repository backed by pg. The caller retains ownership of
// pg and is responsible for closing it; the repository holds only
// references to its fully-qualified table names and shared *sql.DB handle.
func Open(pg *store.PostgresStore) configstore.Repository {
	return &pgRepo{
		db:        pg.DB(),
		schema:    pg.Schema(),
		table:     pg.RuntimeConfigTable(),
		revisions: pg.ConfigRevisionsTable(),
		imports:   pg.ConfigImportsTable(),
	}
}

type pgRepo struct {
	db        *sql.DB
	schema    string
	table     string
	revisions string
	imports   string
}

// Load reads the active runtime_config singleton. When the singleton is
// absent (no accepted save has ever been recorded) Load returns a NewEmpty
// snapshot with Revision 0 and a nil error so callers can build an
// initial candidate without special-casing the empty case.
func (r *pgRepo) Load(ctx context.Context) (*configsnapshot.Snapshot, error) {
	row := r.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT settings, extra, revision, updated_at, updated_by, updated_source
		FROM %s WHERE id = 1
	`, r.table))

	var (
		settingsRaw []byte
		extraRaw    []byte
		revision    int64
		updatedAt   sql.NullTime
		updatedBy   sql.NullString
		updatedSrc  sql.NullString
	)
	if err := row.Scan(&settingsRaw, &extraRaw, &revision, &updatedAt, &updatedBy, &updatedSrc); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return snapshotPtr(configsnapshot.NewEmpty()), nil
		}
		return nil, fmt.Errorf("configstore.pg: load: %w", err)
	}

	snap := configsnapshot.NewEmpty()
	if err := unmarshalMap(settingsRaw, &snap.Settings); err != nil {
		return nil, fmt.Errorf("configstore.pg: decode settings: %w", err)
	}
	if err := unmarshalMap(extraRaw, &snap.Extra); err != nil {
		return nil, fmt.Errorf("configstore.pg: decode extra: %w", err)
	}
	snap.Revision = revision
	if updatedAt.Valid {
		snap.UpdatedAt = updatedAt.Time
	}
	if updatedBy.Valid {
		snap.UpdatedBy = updatedBy.String
	}
	if updatedSrc.Valid {
		snap.UpdatedSource = updatedSrc.String
	}
	return snapshotPtr(snap), nil
}

// Save persists candidate when the current revision matches expected. The
// write is atomic: the runtime_config row is upserted with revision+1 in the
// same transaction that appends a config_revisions row carrying the same
// new revision. Empty candidate.Settings is rejected as a programmer
// error: storing a snapshot with no settings would make the runtime
// configuration unrecoverable.
func (r *pgRepo) Save(ctx context.Context, expected int64, candidate *configsnapshot.Snapshot, audit configstore.SaveAudit) (*configsnapshot.Snapshot, error) {
	if candidate == nil {
		return nil, fmt.Errorf("configstore.pg: candidate is required")
	}
	if len(candidate.Settings) == 0 {
		return nil, fmt.Errorf("configstore.pg: empty settings rejected")
	}
	candidateCopy := *candidate

	settingsJSON, err := marshalMap(candidateCopy.Settings)
	if err != nil {
		return nil, fmt.Errorf("configstore.pg: marshal settings: %w", err)
	}
	extraJSON, err := marshalMap(candidateCopy.Extra)
	if err != nil {
		return nil, fmt.Errorf("configstore.pg: marshal extra: %w", err)
	}

	source := audit.Source
	if source == "" {
		source = "dashboard"
	}

	// Actor is nullable on the database side; map an empty actor to NULL so
	// the repository does not record an empty-string actor. reason is NOT NULL
	// in config_revisions, so use a stable default for callers that omit it.
	actor := audit.Actor
	var actorArg any
	if actor != "" {
		actorArg = actor
	}
	reason := audit.Reason
	if strings.TrimSpace(reason) == "" {
		reason = "configstore update"
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("configstore.pg: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var current int64
	row := tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT revision FROM %s WHERE id = 1 FOR UPDATE`, r.table))
	switch err := row.Scan(&current); {
	case errors.Is(err, sql.ErrNoRows):
		if expected != 0 {
			return nil, &configstore.RevisionConflictError{Current: 0}
		}
		current = 0
	case err != nil:
		return nil, fmt.Errorf("configstore.pg: scan current revision: %w", err)
	default:
		if current != expected {
			return nil, &configstore.RevisionConflictError{Current: current}
		}
	}

	newRevision := current + 1
	var (
		committedAt     time.Time
		committedBy     sql.NullString
		committedSource string
	)
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, settings, extra, revision, updated_at, updated_by, updated_source)
		VALUES (1, $1::jsonb, $2::jsonb, $3, NOW(), $4, $5)
		ON CONFLICT (id) DO UPDATE SET
			settings = EXCLUDED.settings,
			extra = EXCLUDED.extra,
			revision = EXCLUDED.revision,
			updated_at = NOW(),
			updated_by = EXCLUDED.updated_by,
			updated_source = EXCLUDED.updated_source
		RETURNING updated_at, updated_by, updated_source
	`, r.table), settingsJSON, extraJSON, newRevision, actorArg, source).Scan(
		&committedAt, &committedBy, &committedSource,
	); err != nil {
		return nil, fmt.Errorf("configstore.pg: upsert runtime_config: %w", err)
	}

	// resource_snapshot is intentionally empty in Phase 1: Phase 2 introduces
	// the canonical projection of normalised tables. Persisting a literal
	// '{}' keeps the column populated and the checksum stable.
	resourceSnapshot := []byte("{}")
	checksumSnapshot := candidateCopy
	checksumSnapshot.UpdatedAt = committedAt
	if committedBy.Valid {
		checksumSnapshot.UpdatedBy = committedBy.String
	} else {
		checksumSnapshot.UpdatedBy = ""
	}
	checksumSnapshot.UpdatedSource = committedSource
	checksum := checksumSnapshot.Checksum()
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (revision, settings, resource_snapshot, checksum, created_at, created_by, reason)
		VALUES ($1, $2::jsonb, $3::jsonb, $4, NOW(), $5, $6)
	`, r.revisions), newRevision, settingsJSON, resourceSnapshot, checksum, actorArg, reason); err != nil {
		return nil, fmt.Errorf("configstore.pg: insert config_revisions: %w", err)
	}

	// The bootstrap path (expected==0) also records one config_imports audit
	// row in the same transaction, so the dashboard's import history shows
	// how the singleton was first populated. Non-bootstrap saves (dashboard
	// edits, rollbacks) do not touch the import audit trail.
	if expected == 0 {
		importMode := audit.Mode
		if strings.TrimSpace(importMode) == "" {
			importMode = "dashboard"
		}
		var sourceLabel any
		if strings.TrimSpace(audit.ImportSource) != "" {
			sourceLabel = audit.ImportSource
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO %s (source_label, source_checksum, canonical_checksum, status, revision,
			                resource_counts, error_summary, actor, mode)
			VALUES ($1, '', $2, 'committed', $3, '{}'::jsonb, NULL, $4, $5)
		`, r.imports), sourceLabel, checksum, newRevision, actorArg, importMode); err != nil {
			return nil, fmt.Errorf("configstore.pg: insert config_imports audit: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("configstore.pg: commit: %w", err)
	}

	candidateCopy.Revision = newRevision
	candidateCopy.UpdatedAt = committedAt
	if committedBy.Valid {
		candidateCopy.UpdatedBy = committedBy.String
	} else {
		candidateCopy.UpdatedBy = ""
	}
	candidateCopy.UpdatedSource = committedSource
	return snapshotPtr(candidateCopy), nil
}

// Rollback copies the settings stored in config_revisions at targetRevision
// into a new active revision. The active runtime_config row is locked FOR
// UPDATE so concurrent saves cannot interleave; the revision counter is
// incremented by one and updated_source is hard-coded to "rollback" so the
// history records the channel unambiguously. When targetRevision does not
// exist the call returns a descriptive error and the transaction is rolled
// back without writing either table.
func (r *pgRepo) Rollback(ctx context.Context, targetRevision int64, reason, actor string) (*configsnapshot.Snapshot, error) {
	if targetRevision <= 0 {
		return nil, fmt.Errorf("configstore.pg: rollback target revision must be positive; got %d", targetRevision)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("configstore.pg: begin rollback tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var settingsJSON []byte
	row := tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT settings FROM %s WHERE revision = $1`, r.revisions), targetRevision)
	if err := row.Scan(&settingsJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("configstore.pg: rollback target revision %d not found in config_revisions", targetRevision)
		}
		return nil, fmt.Errorf("configstore.pg: scan rollback target: %w", err)
	}

	var current int64
	row = tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT revision FROM %s WHERE id = 1 FOR UPDATE`, r.table))
	switch err := row.Scan(&current); {
	case errors.Is(err, sql.ErrNoRows):
		// Bootstrap path: no active row yet; a rollback creates it.
		current = 0
	case err != nil:
		return nil, fmt.Errorf("configstore.pg: lock current revision: %w", err)
	}

	var actorArg any
	if actor != "" {
		actorArg = actor
	}
	rollbackReason := fmt.Sprintf("rollback to revision %d", targetRevision)
	if reason != "" {
		rollbackReason = fmt.Sprintf("%s: %s", rollbackReason, reason)
	}
	var reasonArg any
	if rollbackReason != "" {
		reasonArg = rollbackReason
	}

	newRevision := current + 1
	var (
		committedAt     time.Time
		committedBy     sql.NullString
		committedSource string
	)
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, settings, extra, revision, updated_at, updated_by, updated_source)
		VALUES (1, $1::jsonb, '{}'::jsonb, $2, NOW(), $3, 'rollback')
		ON CONFLICT (id) DO UPDATE SET
			settings = EXCLUDED.settings,
			extra = EXCLUDED.extra,
			revision = EXCLUDED.revision,
			updated_at = NOW(),
			updated_by = EXCLUDED.updated_by,
			updated_source = EXCLUDED.updated_source
		RETURNING updated_at, updated_by, updated_source
	`, r.table), settingsJSON, newRevision, actorArg).Scan(
		&committedAt, &committedBy, &committedSource,
	); err != nil {
		return nil, fmt.Errorf("configstore.pg: write rollback to runtime_config: %w", err)
	}

	// Rollback entries do not yet compute a meaningful checksum (the
	// snapshot they encode is reconstructed from a prior revision), so the
	// column is stored as empty. Future phases can re-derive the canonical
	// checksum after the rollback is applied.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (revision, settings, resource_snapshot, checksum, created_at, created_by, reason)
		VALUES ($1, $2::jsonb, '{}'::jsonb, '', NOW(), $3, $4)
	`, r.revisions), newRevision, settingsJSON, actorArg, reasonArg); err != nil {
		return nil, fmt.Errorf("configstore.pg: insert rollback config_revisions row: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("configstore.pg: commit rollback: %w", err)
	}

	rollbackSnapshot := configsnapshot.NewEmpty()
	if err := unmarshalMap(settingsJSON, &rollbackSnapshot.Settings); err != nil {
		return nil, fmt.Errorf("configstore.pg: decode rollback settings: %w", err)
	}
	rollbackSnapshot.Extra = map[string]any{}
	rollbackSnapshot.Revision = newRevision
	rollbackSnapshot.UpdatedAt = committedAt
	if committedBy.Valid {
		rollbackSnapshot.UpdatedBy = committedBy.String
	}
	rollbackSnapshot.UpdatedSource = committedSource
	return snapshotPtr(rollbackSnapshot), nil
}

// snapshotPtr returns a pointer to the value. The interface contract
// hands out *configsnapshot.Snapshot values; the value type from
// NewEmpty is converted here so callers do not need to repeat the
// address-of dance.
func snapshotPtr(s configsnapshot.Snapshot) *configsnapshot.Snapshot {
	return &s
}

// marshalMap encodes a map[string]any as JSON. The helper exists so the
// repository centralises the failure mode: a map with non-encodable
// values (functions, channels) is a programmer error wrapped with the
// package prefix.
func marshalMap(m map[string]any) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	buf, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return buf, nil
}

// unmarshalMap decodes JSONB bytes into a map[string]any. The helper
// allocates a non-nil empty map when the input is "null" or empty so the
// snapshot keeps the invariant that Load never returns nil Settings/Extra
// maps.
//
// Decoding goes through json.Decoder.UseNumber() so JSON numbers are
// preserved as json.Number rather than collapsing to float64; the
// normalizeJSON walk then classifies each number into int / int64 /
// uint64 / float64, matching the numeric semantics of
// configsnapshot.UnmarshalYAML (which preserves YAML's int/float
// distinction). Without the normalization a YAML-loaded integer like
// "port: 8317" would round-trip through JSONB as float64 and fail the
// existing snapshot type assertions.
func unmarshalMap(raw []byte, m *map[string]any) error {
	if len(raw) == 0 {
		*m = map[string]any{}
		return nil
	}
	var holder any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&holder); err != nil {
		return err
	}
	normalized, err := normalizeJSONValue(holder)
	if err != nil {
		return err
	}
	if normalized == nil {
		*m = map[string]any{}
		return nil
	}
	out, ok := normalized.(map[string]any)
	if !ok {
		return fmt.Errorf("configstore.pg: top-level jsonb must be an object, got %T", normalized)
	}
	*m = out
	return nil
}

// normalizeJSONValue recursively walks a value decoded with
// json.Decoder.UseNumber() and converts json.Number scalars into the
// narrowest Go numeric type that still preserves the value:
//
//   - Numbers without a fractional part become int when they fit in the
//     platform int range, int64 otherwise, and uint64 when the value is
//     non-negative and fits in uint64. This matches YAML's default
//     integer handling so a YAML round-trip and a PG JSONB round-trip
//     agree on scalar types.
//   - Numbers with a fractional part (or in scientific notation with a
//     negative exponent) become float64.
//
// Maps are normalized as map[string]any; slices and the rest pass through
// unchanged. JSON object keys are strings by definition, so this walk keeps
// the JSONB shape without any key coercion.
func normalizeJSONValue(v any) (any, error) {
	switch t := v.(type) {
	case json.Number:
		return classifyJSONNumber(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			nv, err := normalizeJSONValue(val)
			if err != nil {
				return nil, err
			}
			out[k] = nv
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			nv, err := normalizeJSONValue(item)
			if err != nil {
				return nil, err
			}
			out[i] = nv
		}
		return out, nil
	default:
		return v, nil
	}
}

// classifyJSONNumber converts a json.Number into the narrowest Go numeric
// type that preserves the value. Integer strings become int when the value
// fits in the platform int range, int64 otherwise, or uint64 for non-negative
// values above MaxInt64. Decimal and exponent forms become float64, matching
// the distinction made by configsnapshot.UnmarshalYAML between YAML integer
// and floating-point scalars.
func classifyJSONNumber(n json.Number) (any, error) {
	s := n.String()
	if !strings.ContainsAny(s, ".eE") {
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			maxInt := int64(^uint(0) >> 1)
			minInt := -maxInt - 1
			if i >= minInt && i <= maxInt {
				return int(i), nil
			}
			return i, nil
		}
		if u, err := strconv.ParseUint(s, 10, 64); err == nil {
			return u, nil
		}
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f, nil
	}
	return nil, fmt.Errorf("configstore.pg: cannot decode json number %q", s)
}
