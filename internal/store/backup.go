package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// BackupResource identifies a logical group of tables that can be exported and
// imported independently. Each resource maps to one or more backing tables, and
// the set is deliberately kept stable so an export produced by one NixLLM
// version can be imported by a later one.
type BackupResource string

const (
	// ResourceAPIKeys covers client-facing API keys and their per-key policies.
	ResourceAPIKeys BackupResource = "api_keys"
	// ResourceInternalUsers covers internal users and their budget windows.
	ResourceInternalUsers BackupResource = "internal_users"
	// ResourceModelGroups covers reusable Model Group templates.
	ResourceModelGroups BackupResource = "model_groups"
	// ResourceAutoRouters covers Auto Router entity definitions.
	ResourceAutoRouters BackupResource = "auto_routers"
	// ResourceModels covers the model catalog (models_catalog + model_pricing).
	ResourceModels BackupResource = "models_catalog"
	// ResourceUpstreamProviders covers normalized upstream providers and their
	// child tables (models, headers, excluded models, api-key entries).
	ResourceUpstreamProviders BackupResource = "upstream_providers"
	// ResourceProxyPools covers the named egress-proxy pools bound to
	// upstream provider rows/entries via proxy_pool_id.
	ResourceProxyPools BackupResource = "proxy_pools"
	// ResourceManagementTokens covers management API tokens and per-token policies.
	ResourceManagementTokens BackupResource = "management_tokens"
	// ResourceErrorMessages covers operator-customized error responses.
	ResourceErrorMessages BackupResource = "error_messages"
	// ResourcePricingSources covers external pricing catalog sources (including
	// the on-disk file for source_type=file, embedded as base64).
	ResourcePricingSources BackupResource = "pricing_sources"
	// ResourceAuthFiles covers the auth_store rows (OAuth/file-backed auths).
	ResourceAuthFiles BackupResource = "auth_files"
	// ResourceUsage covers usage_events, usage_errors and usage_windows.
	ResourceUsage BackupResource = "usage"
	// ResourceAlerts covers the alerts feed and alert_settings.
	ResourceAlerts BackupResource = "alerts"
	// ResourceModelHealth covers model_health snapshots, log, and settings.
	ResourceModelHealth BackupResource = "model_health"
	// ResourceSyncLog covers the upstream_sync_log table.
	ResourceSyncLog BackupResource = "sync_log"
	// ResourceConfigStore covers the PG mirror of config.yaml (config_store).
	ResourceConfigStore BackupResource = "config_store"
	// ResourceRuntimeConfig covers the PG-first runtime_config singleton
	// (one row: settings/extra JSONB + revision counter + update metadata).
	ResourceRuntimeConfig BackupResource = "runtime_config"
	// ResourceConfigRevisions covers the append-only config_revisions
	// history backing rollback and audit.
	ResourceConfigRevisions BackupResource = "config_revisions"
	// ResourceConfigImports covers the config_imports audit table (one row
	// per import attempt).
	ResourceConfigImports BackupResource = "config_imports"
	// ResourceCooldownStore covers runtime auth/model cooldown state
	// (cooldown_store).
	ResourceCooldownStore BackupResource = "cooldown_store"
	// ResourceUsageStatDay covers the pre-aggregated daily usage rollup
	// (usage_stat_day).
	ResourceUsageStatDay BackupResource = "usage_stat_day"
	// ResourceModelRouting covers per-model-id global routing overrides
	// (model_routing).
	ResourceModelRouting BackupResource = "model_routing"
	// ResourceManagementAuditLog covers the management API audit trail
	// (management_audit_log).
	ResourceManagementAuditLog BackupResource = "management_audit_log"
)

// importBatchSize is the maximum number of rows restored per multi-row INSERT.
// Config tables restore inside a single transaction regardless of size; data
// tables (usage, alerts, model_health, sync_log) commit one transaction per
// batch so huge tables import in bounded chunks with progress callbacks.
const importBatchSize = 1000

// dataResource reports whether res belongs to the chunked data set. Data tables
// can grow large, so their import is split into per-chunk transactions with a
// progress callback and continue-to-end soft-failure handling. All other
// resources are config tables restored atomically in a single transaction.
func dataResource(res BackupResource) bool {
	return IsBackupDataResource(res)
}

// IsBackupDataResource reports whether res belongs to the chunked data set
// (see dataResource). Exported so backup/restore orchestration can distinguish
// data resources (safe to merge) from config resources (always wipe+replace).
func IsBackupDataResource(res BackupResource) bool {
	switch res {
	case ResourceUsage, ResourceAlerts, ResourceModelHealth, ResourceSyncLog,
		ResourceUsageStatDay, ResourceManagementAuditLog:
		return true
	default:
		return false
	}
}

// AllBackupResources is the canonical ordered list of every backup resource,
// parents before children so that import order satisfies cross-table
// references (best-effort; the DB does not enforce most FKs).
var AllBackupResources = []BackupResource{
	ResourceAPIKeys,
	ResourceInternalUsers,
	ResourceModelGroups,
	ResourceAutoRouters,
	ResourceModels,
	ResourceUpstreamProviders,
	ResourceProxyPools,
	ResourceManagementTokens,
	ResourceErrorMessages,
	ResourcePricingSources,
	ResourceAuthFiles,
	ResourceUsage,
	ResourceAlerts,
	ResourceModelHealth,
	ResourceSyncLog,
	ResourceConfigStore,
	ResourceRuntimeConfig,
	ResourceConfigRevisions,
	ResourceConfigImports,
	ResourceCooldownStore,
	ResourceUsageStatDay,
	ResourceModelRouting,
	ResourceManagementAuditLog,
}

// ValidBackupResource reports whether s names a known backup resource.
func ValidBackupResource(s string) bool {
	for _, r := range AllBackupResources {
		if string(r) == s {
			return true
		}
	}
	return false
}

// backupTable describes one table within a resource along with the column
// (if any) used as the stable ordering key during dump.
type backupTable struct {
	// name is the fully-qualified table name.
	name string
	// orderColumn is an optional column used to order rows during dump so the
	// export is deterministic. Empty means a heap scan (order not guaranteed).
	orderColumn string
}

// backupResource describes one logical group of tables, parents first.
type backupResource struct {
	key    BackupResource
	tables []backupTable
}

// resourceTables returns the ordered table list for a single resource using
// the store's configured (schema-qualified) table accessors. The auth table
// has no dedicated accessor, so it is derived from the config's AuthTable.
func (s *PostgresStore) resourceTables(res BackupResource) []backupTable {
	authTable := s.fullTableName(s.cfg.AuthTable)
	switch res {
	case ResourceAPIKeys:
		return []backupTable{
			{s.APIKeysTable(), "id"},
			{s.PoliciesTable(), "api_key_id"},
		}
	case ResourceInternalUsers:
		return []backupTable{
			{s.InternalUsersTable(), "id"},
			{s.UserWindowsTable(), ""},
		}
	case ResourceModelGroups:
		return []backupTable{{s.ModelGroupsTable(), "id"}}
	case ResourceAutoRouters:
		return []backupTable{{s.AutoRoutersTable(), "id"}}
	case ResourceModels:
		return []backupTable{
			{s.ModelsTable(), ""},
			{s.ModelPricingTable(), "id"},
		}
	case ResourceUpstreamProviders:
		return []backupTable{
			{s.UpstreamProvidersTable(), "id"},
			{s.UpstreamProviderModelsTable(), "id"},
			{s.UpstreamProviderHeadersTable(), ""},
			{s.UpstreamProviderExcludedTable(), ""},
			{s.UpstreamProviderEntriesTable(), "id"},
		}
	case ResourceProxyPools:
		return []backupTable{{s.ProxyPoolsTable(), "id"}}
	case ResourceManagementTokens:
		return []backupTable{
			{s.ManagementTokensTable(), "id"},
			{s.ManagementTokenPoliciesTable(), "token_id"},
		}
	case ResourceErrorMessages:
		return []backupTable{{s.ErrorMessagesTable(), "status_code"}}
	case ResourcePricingSources:
		return []backupTable{{s.PricingSourcesTable(), "id"}}
	case ResourceAuthFiles:
		return []backupTable{{authTable, "id"}}
	case ResourceUsage:
		return []backupTable{
			{s.UsageEventsTable(), "id"},
			{s.UsageErrorsTable(), "id"},
			{s.UsageWindowsTable(), ""},
		}
	case ResourceAlerts:
		return []backupTable{
			{s.AlertsTable(), "id"},
			{s.AlertSettingsTable(), "id"},
		}
	case ResourceModelHealth:
		return []backupTable{
			{s.ModelHealthTable(), ""},
			{s.ModelHealthLogTable(), "id"},
			{s.ModelHealthSettingsTable(), "id"},
		}
	case ResourceSyncLog:
		return []backupTable{{s.UpstreamSyncLogTable(), "id"}}
	case ResourceConfigStore:
		return []backupTable{{s.ConfigTable(), "id"}}
	case ResourceRuntimeConfig:
		return []backupTable{{s.RuntimeConfigTable(), "id"}}
	case ResourceConfigRevisions:
		return []backupTable{{s.ConfigRevisionsTable(), "revision"}}
	case ResourceConfigImports:
		return []backupTable{{s.ConfigImportsTable(), "id"}}
	case ResourceCooldownStore:
		return []backupTable{{s.CooldownTable(), ""}}
	case ResourceUsageStatDay:
		// usage_stat_day has no surrogate id column; order by stat_day so the
		// dump is deterministic.
		return []backupTable{{s.RollupTable(), "stat_day"}}
	case ResourceModelRouting:
		return []backupTable{{s.ModelRoutingTable(), "id"}}
	case ResourceManagementAuditLog:
		return []backupTable{{s.ManagementAuditLogTable(), "id"}}
	default:
		return nil
	}
}

// backupBundleVersion is the current BackupBundle format version. Version 2
// adds the config_yaml and auth_files sections for full-backup coverage.
const backupBundleVersion = 2

// BackupAuthFile carries one auths/ file (OAuth/file-backed credential) from
// the active store's AuthDir, keyed by its path relative to AuthDir.
type BackupAuthFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// BackupBundle is the portable JSON document produced by ExportData and consumed
// by ImportData. Resources maps a resource key to a per-table set of rows.
// Summary carries a per-resource total row count so the frontend can render a
// pre-import preview without parsing every row.
type BackupBundle struct {
	Version    int                           `json:"version"`
	ExportedAt time.Time                     `json:"exported_at"`
	Mode       string                        `json:"mode,omitempty"`
	Summary    map[string]int                `json:"summary,omitempty"`
	Resources  map[string]BackupResourceData `json:"resources"`
	// ConfigYAML is the full contents of the active config.yaml, embedded so a
	// full backup restores provider credentials, model routing, and all other
	// file-based settings that the PG tables do not cover.
	ConfigYAML string `json:"config_yaml,omitempty"`
	// AuthFiles is the set of auths/ files (OAuth/file-backed credentials)
	// present in the active store's AuthDir at backup time.
	AuthFiles []BackupAuthFile `json:"auth_files,omitempty"`
}

// rowCount sums the exported rows across all tables of one resource data set.
func rowCount(data BackupResourceData) int {
	n := 0
	for _, rows := range data.Tables {
		n += len(rows)
	}
	return n
}

// BackupResourceData holds the exported rows for one resource. Tables maps a
// table name to its exported rows; Files carries base64-encoded on-disk
// payloads (currently only pricing-source catalog files) keyed by the absolute
// path recorded in the corresponding table row.
type BackupResourceData struct {
	Tables map[string][]json.RawMessage `json:"tables,omitempty"`
	Files  map[string]string            `json:"files,omitempty"`
}

// BackupExportOpts controls ExportData. Resources empty/nil means all.
type BackupExportOpts struct {
	Resources []BackupResource
}

// BackupImportOpts controls ImportData. Resources empty/nil means all resources
// present in the bundle.
type BackupImportOpts struct {
	Resources []BackupResource
	// Progress, when non-nil, reports the cumulative number of rows restored so
	// far, across all resources. It fires once after the config phase commits
	// (with the config row total, so a bar scaled to the bundle summary reaches
	// 100% when all data rows also land), and again after each data chunk with
	// the running total including config rows. It never fires on the config
	// failure path, since nothing was committed.
	Progress func(inserted int)
}

// BackupImportReport summarizes the per-resource outcome of ImportData.
type BackupImportReport struct {
	Resources map[BackupResource]BackupImportResourceReport `json:"resources,omitempty"`
	Total     int                                           `json:"total_inserted"`
	// Partial is set when any chunked data resource skipped a row or chunk, i.e.
	// the destination holds a subset of the bundle's data rows.
	Partial bool `json:"partial,omitempty"`
}

// BackupImportResourceReport reports how many rows were inserted, skipped, and
// the first error (if any) for a single resource.
type BackupImportResourceReport struct {
	Inserted int    `json:"inserted"`
	Skipped  int    `json:"skipped"`
	Error    string `json:"error,omitempty"`
}

// sealer returns a Sealer derived from the store's configured encryption key,
// or nil when encryption is disabled.
func (s *PostgresStore) sealer() *Sealer {
	sealer, err := NewSealer(s.cfg.UsageEncryptionKey)
	if err != nil {
		log.WithError(err).Warn("postgres store: backup sealer unavailable; using plaintext pass-through")
		return nil
	}
	return sealer
}

// sealedColumnsByTable maps a default table name to the set of columns that are
// AES-GCM-sealed at rest (encrypted by the source Sealer at write time). Export
// unseals these so the bundle is portable plaintext; Import re-seals with the
// destination Sealer. When the sealer is nil these columns are plaintext and
// the transform is a no-op.
var sealedColumnsByTable = map[string][]string{
	defaultUsageEventsTable:        {"api_key_principal"},
	defaultUsageErrorsTable:        {"api_key_principal"},
	defaultUpstreamSyncLogTable:    {"error_message"},
	defaultModelHealthTable:        {"error_message", "prompt_message", "completion"},
	defaultModelHealthLogTable:     {"error_message", "prompt_message", "completion"},
	defaultAlertsTable:             {"message"},
	defaultManagementAuditLogTable: {"request_body", "response_body"},
}

// exportTable dumps every row of one table as json.RawMessage objects, applying
// the unseal transform to sealed columns. Each row is encoded via to_jsonb so
// the raw bytes round-trip losslessly for every column type.
func (s *PostgresStore) exportTable(ctx context.Context, sealer *Sealer, bt backupTable) ([]json.RawMessage, error) {
	cols := sealedColumnsByTable[defaultTableHint(bt.name)]
	query := fmt.Sprintf("SELECT to_jsonb(t)::text FROM %s t", bt.name)
	if bt.orderColumn != "" {
		query += " ORDER BY t." + quoteIdentifier(bt.orderColumn)
	}
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("postgres store: export %s: %w", bt.name, err)
	}
	defer rows.Close()

	var out []json.RawMessage
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return nil, fmt.Errorf("postgres store: export %s scan: %w", bt.name, err)
		}
		raw := json.RawMessage(text)
		if len(cols) > 0 {
			transformed, err := transformSealedColumns(sealer, raw, cols, true)
			if err != nil {
				return nil, fmt.Errorf("postgres store: export %s unseal: %w", bt.name, err)
			}
			raw = transformed
		}
		out = append(out, raw)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres store: export %s iterate: %w", bt.name, err)
	}
	return out, nil
}

// importTable wipes and restores one table from its exported rows within the
// caller's transaction. This is used for config resources, which are
// all-or-nothing: any wipe, seal, or insert failure aborts the caller's
// transaction so nothing in it is committed. Rows are inserted in batches of
// importBatchSize via multi-row jsonb_populate_recordset so column types are
// preserved.
func (s *PostgresStore) importTable(ctx context.Context, tx *sql.Tx, sealer *Sealer, bt backupTable, rows []json.RawMessage) (int, int, error) {
	if _, err := tx.ExecContext(ctx, "DELETE FROM "+bt.name); err != nil {
		return 0, 0, fmt.Errorf("postgres store: import wipe %s: %w", bt.name, err)
	}
	inserted := 0
	for start := 0; start < len(rows); start += importBatchSize {
		end := start + importBatchSize
		if end > len(rows) {
			end = len(rows)
		}
		if err := s.insertRows(ctx, tx, sealer, bt, rows[start:end]); err != nil {
			return inserted, 0, err
		}
		inserted += end - start
	}
	return inserted, 0, nil
}

// insertRows executes one multi-row INSERT of rows into bt.name via
// jsonb_populate_recordset, applying the seal transform to sealed columns. The
// whole statement is atomic: a single malformed row fails the entire batch.
func (s *PostgresStore) insertRows(ctx context.Context, tx *sql.Tx, sealer *Sealer, bt backupTable, rows []json.RawMessage) error {
	cols := sealedColumnsByTable[defaultTableHint(bt.name)]
	arr := make([]json.RawMessage, 0, len(rows))
	for _, raw := range rows {
		final := raw
		if len(cols) > 0 {
			transformed, err := transformSealedColumns(sealer, raw, cols, false)
			if err != nil {
				return fmt.Errorf("postgres store: import %s seal: %w", bt.name, err)
			}
			final = transformed
		}
		arr = append(arr, final)
	}
	payload, err := json.Marshal(arr)
	if err != nil {
		return fmt.Errorf("postgres store: import %s marshal chunk: %w", bt.name, err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		"INSERT INTO %s SELECT * FROM jsonb_populate_recordset(NULL::%s, $1::jsonb)", bt.name, bt.name),
		string(payload),
	); err != nil {
		return fmt.Errorf("postgres store: import insert %s: %w", bt.name, err)
	}
	return nil
}

// transformSealedColumns unseals (unseal=true, export) or seals (unseal=false,
// import) the named columns of a single jsonb row. A nil sealer is a no-op.
func transformSealedColumns(sealer *Sealer, raw json.RawMessage, cols []string, unseal bool) (json.RawMessage, error) {
	if sealer == nil || len(cols) == 0 {
		return raw, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	found := false
	for _, col := range cols {
		v, ok := m[col]
		if !ok {
			continue
		}
		str, ok := v.(string)
		if !ok || str == "" {
			continue
		}
		found = true
		var out string
		var err error
		if unseal {
			out, err = sealer.Open(str)
		} else {
			out, err = sealer.Seal(str)
		}
		if err == nil {
			m[col] = out
		}
		// On error, leave the original value intact (best-effort).
	}
	if !found {
		return raw, nil
	}
	enc, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(enc), nil
}

// exportResource dumps every table of one resource into BackupResourceData,
// applying the unseal transform to sealed columns and embedding pricing-source
// catalog files. It is the shared row fixture for both ExportData and
// StreamExport.
func (s *PostgresStore) exportResource(ctx context.Context, sealer *Sealer, res BackupResource) (BackupResourceData, error) {
	data := BackupResourceData{Tables: make(map[string][]json.RawMessage), Files: make(map[string]string)}
	for _, bt := range s.resourceTables(res) {
		rows, err := s.exportTable(ctx, sealer, bt)
		if err != nil {
			return data, err
		}
		if len(rows) > 0 {
			data.Tables[bt.name] = rows
		}
	}
	// Pricing-source catalog files live on disk, not in the DB. Embed their
	// content so a file-backed catalog survives the round-trip.
	if res == ResourcePricingSources {
		if err := s.embedPricingSourceFiles(ctx, &data); err != nil {
			return data, err
		}
	}
	return data, nil
}

// ExportData produces a portable BackupBundle for the requested resources
// (default all). Within each resource, tables are ranked parents-first
// (already reflected in resourceTables). Sealed columns are unsealed so the
// bundle is portable across instances regardless of encryption keys.
func (s *PostgresStore) ExportData(ctx context.Context, opts BackupExportOpts) (BackupBundle, error) {
	bundle := BackupBundle{
		Version:    backupBundleVersion,
		ExportedAt: time.Now().UTC(),
		Summary:    make(map[string]int),
		Resources:  make(map[string]BackupResourceData),
	}
	if s == nil || s.db == nil {
		return bundle, fmt.Errorf("postgres store: backup not initialized")
	}
	sealer := s.sealer()
	resources := opts.Resources
	if len(resources) == 0 {
		resources = AllBackupResources
	}
	for _, res := range resources {
		data, err := s.exportResource(ctx, sealer, res)
		if err != nil {
			return bundle, err
		}
		bundle.Resources[string(res)] = data
		bundle.Summary[string(res)] = rowCount(data)
	}
	return bundle, nil
}

// StreamExport writes the same BackupBundle document as ExportData but streams
// it to w progressively, table by table, instead of assembling the whole bundle
// in memory. Row bodies are written one row at a time straight from the DB
// cursor, so memory stays bounded regardless of table size. The per-resource
// header summary is computed up front with cheap COUNT(*) queries (the body must
// follow the header, so the counts have to be known before the rows are
// streamed). The emitted JSON round-trips to a valid BackupBundle.
func (s *PostgresStore) StreamExport(ctx context.Context, opts BackupExportOpts, w io.Writer) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: backup not initialized")
	}
	sealer := s.sealer()
	resources := opts.Resources
	if len(resources) == 0 {
		resources = AllBackupResources
	}

	// The header's per-resource summary precedes the row bodies, so count each
	// resource's rows up front. COUNT(*) scans the table but materializes no
	// rows, keeping the heavy to_jsonb dump single-pass below.
	summary := make(map[string]int, len(resources))
	for _, res := range resources {
		n, err := s.countResourceRows(ctx, res)
		if err != nil {
			return err
		}
		summary[string(res)] = n
	}

	// {"version":1,"exported_at":"...","summary":{...},"resources":{
	if err := writeStreamHeader(w, summary, time.Now().UTC()); err != nil {
		return err
	}

	for i, res := range resources {
		if i > 0 {
			if _, err := io.WriteString(w, ","); err != nil {
				return err
			}
		}
		if err := writeJSONKey(w, string(res)); err != nil {
			return err
		}
		if _, err := io.WriteString(w, ":"); err != nil {
			return err
		}
		if err := s.writeStreamResource(ctx, sealer, w, res); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "}}")
	return err
}

// writeStreamHeader writes the bundle's opening through the "resources" key,
// i.e. {"version":1,"exported_at":"...","summary":{...},"resources":{
func writeStreamHeader(w io.Writer, summary map[string]int, exportedAt time.Time) error {
	header := struct {
		Version    int            `json:"version"`
		ExportedAt time.Time      `json:"exported_at"`
		Summary    map[string]int `json:"summary"`
	}{
		Version:    1,
		ExportedAt: exportedAt,
		Summary:    summary,
	}
	b, err := json.Marshal(header)
	if err != nil {
		return fmt.Errorf("postgres store: stream export marshal header: %w", err)
	}
	// Strip the header object's closing brace and open the resources object so
	// per-resource bodies can be appended incrementally.
	if _, err := w.Write(b[:len(b)-1]); err != nil {
		return err
	}
	_, err = io.WriteString(w, `,"resources":{`)
	return err
}

// countResourceRows sums the row counts of every table in one resource via cheap
// COUNT(*) queries, mirroring the per-resource total that ExportData derives
// from the dumped rows. Note the COUNT pass and the later to_jsonb dump pass each
// see their own READ COMMITTED snapshot, so a concurrent write between them can
// make the header summary diverge slightly from the dumped rows; the impact is
// cosmetic because the summary only drives the frontend preview, while import
// consumes the row bodies.
func (s *PostgresStore) countResourceRows(ctx context.Context, res BackupResource) (int, error) {
	n := 0
	for _, bt := range s.resourceTables(res) {
		var c int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+bt.name).Scan(&c); err != nil {
			return 0, fmt.Errorf("postgres store: count %s: %w", bt.name, err)
		}
		n += c
	}
	return n, nil
}

// writeStreamResource streams one resource's body: {"tables":{...}} with an
// optional ,"files":{...} for pricing-source catalog files. Only tables that
// have at least one row are emitted, matching ExportData.
func (s *PostgresStore) writeStreamResource(ctx context.Context, sealer *Sealer, w io.Writer, res BackupResource) error {
	if _, err := io.WriteString(w, `{"tables":{`); err != nil {
		return err
	}
	wroteAnyTable := false
	for _, bt := range s.resourceTables(res) {
		wrote, err := s.writeTableRows(ctx, sealer, w, bt, wroteAnyTable)
		if err != nil {
			return err
		}
		if wrote {
			wroteAnyTable = true
		}
	}
	if _, err := io.WriteString(w, `}`); err != nil {
		return err
	}
	// Pricing-source catalog files live on disk, not in the DB. Embed their
	// content so a file-backed catalog survives the round-trip.
	if res == ResourcePricingSources {
		if err := s.writeStreamPricingFiles(ctx, w); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, `}`)
	return err
}

// writeTableRows streams one table's rows to w as its JSON key followed by a
// JSON array, e.g. "usage_events":[{...},{...}]. Each row is written as it is
// scanned and unsealed, so memory stays bounded to one row regardless of table
// size. When leadingComma is true and the table has rows, a separating comma is
// written first so consecutive tables form a valid JSON object. It returns
// whether any rows were written (false for an empty table, which is skipped).
func (s *PostgresStore) writeTableRows(ctx context.Context, sealer *Sealer, w io.Writer, bt backupTable, leadingComma bool) (bool, error) {
	cols := sealedColumnsByTable[defaultTableHint(bt.name)]
	query := fmt.Sprintf("SELECT to_jsonb(t)::text FROM %s t", bt.name)
	if bt.orderColumn != "" {
		query += " ORDER BY t." + quoteIdentifier(bt.orderColumn)
	}
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return false, fmt.Errorf("postgres store: export %s: %w", bt.name, err)
	}
	defer rows.Close()

	wrote := false
	if rows.Next() {
		wrote = true
		if leadingComma {
			if _, err := io.WriteString(w, ","); err != nil {
				return false, err
			}
		}
		if err := writeJSONKey(w, bt.name); err != nil {
			return false, err
		}
		if _, err := io.WriteString(w, ":["); err != nil {
			return false, err
		}
		if err := s.writeStreamRow(w, sealer, cols, rows, bt.name); err != nil {
			return false, err
		}
	}
	for rows.Next() {
		if _, err := io.WriteString(w, ","); err != nil {
			return false, err
		}
		if err := s.writeStreamRow(w, sealer, cols, rows, bt.name); err != nil {
			return false, err
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("postgres store: export %s iterate: %w", bt.name, err)
	}
	if wrote {
		if _, err := io.WriteString(w, "]"); err != nil {
			return false, err
		}
	}
	return wrote, nil
}

// writeStreamRow scans the current row of rows, applies the unseal transform to
// sealed columns, and writes the resulting JSON verbatim to w.
func (s *PostgresStore) writeStreamRow(w io.Writer, sealer *Sealer, cols []string, rows *sql.Rows, table string) error {
	var text string
	if err := rows.Scan(&text); err != nil {
		return fmt.Errorf("postgres store: export %s scan: %w", table, err)
	}
	raw := json.RawMessage(text)
	if len(cols) > 0 {
		transformed, err := transformSealedColumns(sealer, raw, cols, true)
		if err != nil {
			return fmt.Errorf("postgres store: export %s unseal: %w", table, err)
		}
		raw = transformed
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	return nil
}

// writeStreamPricingFiles queries the pricing-sources table for file-backed
// sources, reads each catalog file off disk, and writes the base64-encoded
// contents as a ,"files":{...} object. No-op (no output) when there are no
// file-backed sources, matching ExportData's omitempty files field.
func (s *PostgresStore) writeStreamPricingFiles(ctx context.Context, w io.Writer) error {
	table := s.PricingSourcesTable()
	query := fmt.Sprintf(
		"SELECT file_path FROM %s WHERE source_type = 'file' AND file_path IS NOT NULL AND file_path != ''",
		table)
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("postgres store: export %s files: %w", table, err)
	}
	defer rows.Close()

	files := make(map[string]string)
	for rows.Next() {
		var filePath string
		if err := rows.Scan(&filePath); err != nil {
			return fmt.Errorf("postgres store: export %s files scan: %w", table, err)
		}
		content, err := os.ReadFile(filePath)
		if err != nil {
			log.WithError(err).WithField("path", filePath).Warn("postgres store: export: pricing source file unreadable")
			continue
		}
		files[filePath] = base64.StdEncoding.EncodeToString(content)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres store: export %s files iterate: %w", table, err)
	}
	if len(files) == 0 {
		return nil
	}
	filesJSON, err := json.Marshal(files)
	if err != nil {
		return fmt.Errorf("postgres store: export %s files marshal: %w", table, err)
	}
	if _, err := io.WriteString(w, `,"files":`); err != nil {
		return err
	}
	_, err = w.Write(filesJSON)
	return err
}

// writeJSONKey writes s as a JSON object key, i.e. a quoted and escaped string.
func writeJSONKey(w io.Writer, s string) error {
	b, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("postgres store: stream export marshal key %q: %w", s, err)
	}
	_, err = w.Write(b)
	return err
}

// embedPricingSourceFiles reads each source_type=file pricing source's on-disk
// catalog and stores it base64-encoded in the resource data, keyed by the
// absolute file_path recorded in the row.
func (s *PostgresStore) embedPricingSourceFiles(ctx context.Context, data *BackupResourceData) error {
	table := s.PricingSourcesTable()
	rows, _ := data.Tables[table]
	for _, raw := range rows {
		var row struct {
			SourceType string `json:"source_type"`
			FilePath   string `json:"file_path"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			continue
		}
		if row.SourceType != "file" || row.FilePath == "" {
			continue
		}
		content, err := os.ReadFile(row.FilePath)
		if err != nil {
			log.WithError(err).WithField("path", row.FilePath).Warn("postgres store: export: pricing source file unreadable")
			continue
		}
		data.Files[row.FilePath] = base64.StdEncoding.EncodeToString(content)
	}
	return nil
}

// ImportData restores the requested resources (default all present) from a
// BackupBundle. The import is hybrid:
//
//   - Config resources (api_keys, internal_users, model_groups, auto_routers,
//     models_catalog, upstream_providers, management_tokens, error_messages,
//     pricing_sources, auth_files) are wiped and re-inserted atomically inside
//     one transaction: any failure rolls the whole config restore back.
//   - Data resources (usage, alerts, model_health, sync_log) are restored
//     resource-by-resource in per-chunk transactions of up to importBatchSize
//     rows each, with the Progress callback invoked after every chunk. A chunk
//     that fails is skipped and reported; the import continues to the next chunk
//     and the report is flagged Partial. Individual data rows are never
//     re-inserted row-by-row, so a bad chunk is skipped wholesale.
//
// Config resources are restored before data resources because data tables
// reference config tables (e.g. usage_windows.api_key_id -> api_keys.id), so
// the FK parents must exist first. Each resource's tables are wiped in reverse
// order (children before parents).
func (s *PostgresStore) ImportData(ctx context.Context, bundle BackupBundle, opts BackupImportOpts) (report BackupImportReport, err error) {
	report = BackupImportReport{Resources: make(map[BackupResource]BackupImportResourceReport)}
	if s == nil || s.db == nil {
		return report, fmt.Errorf("postgres store: backup not initialized")
	}
	sealer := s.sealer()
	resources := opts.Resources
	if len(resources) == 0 {
		for _, r := range AllBackupResources {
			if _, ok := bundle.Resources[string(r)]; ok {
				resources = append(resources, r)
			}
		}
	}

	// Config resources first, all-or-nothing in a single transaction. The total
	// is accumulated locally and only folded into report.Total once the config
	// transaction commits; a config failure returns an error and the caller
	// discards the report, so a half-applied Total must never be reported.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return report, fmt.Errorf("postgres store: import begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	configTotal := 0
	for _, res := range resources {
		if dataResource(res) {
			continue
		}
		data, ok := bundle.Resources[string(res)]
		if !ok {
			continue
		}
		if err := s.wipeResourceTables(ctx, tx, res, &data); err != nil {
			return report, err
		}
		r, err := s.importConfigResource(ctx, tx, sealer, res, &data)
		if err != nil {
			r.Error = err.Error()
			report.Resources[res] = r
			return report, err
		}
		report.Resources[res] = r
		configTotal += r.Inserted
	}

	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("postgres store: import commit: %w", err)
	}
	report.Total = configTotal
	if opts.Progress != nil {
		opts.Progress(configTotal)
	}

	// Data resources afterwards, per-chunk transactions with continue-to-end
	// soft-failure handling.
	progressBase := configTotal
	for _, res := range resources {
		if !dataResource(res) {
			continue
		}
		data, ok := bundle.Resources[string(res)]
		if !ok {
			continue
		}
		r, partial := s.importDataResource(ctx, sealer, res, &data, opts.Progress, progressBase)
		if partial {
			report.Partial = true
		}
		report.Resources[res] = r
		report.Total += r.Inserted
		progressBase += r.Inserted
	}
	return report, nil
}

// wipeResourceTables deletes every table of one resource that is present in the
// bundle data, in reverse table order (children before parents). It runs inside
// the caller's transaction for config resources.
func (s *PostgresStore) wipeResourceTables(ctx context.Context, tx *sql.Tx, res BackupResource, data *BackupResourceData) error {
	tables := s.resourceTables(res)
	for j := len(tables) - 1; j >= 0; j-- {
		if _, ok := data.Tables[tables[j].name]; !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+tables[j].name); err != nil {
			return fmt.Errorf("postgres store: import wipe %s: %w", tables[j].name, err)
		}
	}
	return nil
}

// importConfigResource restores one config resource's tables in order (parents
// before children) inside the caller's single transaction. Any failure aborts
// the caller's transaction (all-or-nothing).
func (s *PostgresStore) importConfigResource(ctx context.Context, tx *sql.Tx, sealer *Sealer, res BackupResource, data *BackupResourceData) (BackupImportResourceReport, error) {
	r := BackupImportResourceReport{}
	for _, bt := range s.resourceTables(res) {
		rows, ok := data.Tables[bt.name]
		if !ok || len(rows) == 0 {
			continue
		}
		inserted, skipped, err := s.importTable(ctx, tx, sealer, bt, rows)
		r.Inserted += inserted
		r.Skipped += skipped
		if err != nil {
			return r, err
		}
		// Rows are restored with their original serial ids, so the id
		// sequence is still sitting far below the highest imported id.
		// Advance it to max(id)+1 so subsequent auto-generated ids never
		// collide with (and silently fail on) restored rows. No-op for
		// tables without a serial id column.
		s.resyncIdSequence(ctx, tx, bt.name)
	}
	// Restore any pricing-source catalog files referenced by the imported rows.
	if res == ResourcePricingSources {
		if err := s.restorePricingSourceFiles(ctx, tx, data); err != nil {
			return r, err
		}
	}
	return r, nil
}

// importDataResource restores one data resource's tables in per-chunk
// transactions. Each chunk (up to importBatchSize rows) gets its own
// transaction: the first chunk of the first table also wipes that table. A
// failed chunk is skipped and reported; import continues with the next chunk.
// The returned partial flag is set when any chunk was skipped. progressBase is
// the count of rows restored before this resource (config rows plus any earlier
// data resources); it is added to each chunk's cumulative count so the Progress
// callback stays monotonic across table and resource boundaries.
func (s *PostgresStore) importDataResource(ctx context.Context, sealer *Sealer, res BackupResource, data *BackupResourceData, progress func(int), progressBase int) (BackupImportResourceReport, bool) {
	r := BackupImportResourceReport{}
	partial := false
	var firstErr error
	totalInserted := 0
	for _, bt := range s.resourceTables(res) {
		rows, ok := data.Tables[bt.name]
		if !ok || len(rows) == 0 {
			continue
		}
		inserted, skipped, chunkErr := s.importChunkedTable(ctx, sealer, bt, rows, func(n int) {
			if progress != nil {
				progress(progressBase + totalInserted + n)
			}
		})
		r.Inserted += inserted
		r.Skipped += skipped
		totalInserted += inserted
		if chunkErr != nil {
			partial = true
			if firstErr == nil {
				firstErr = chunkErr
			}
		}
		// Rows are restored with their original serial ids; resync the sequence
		// in its own transaction so subsequent auto-generated ids never collide.
		if err := s.resyncTableSequence(ctx, bt.name); err != nil {
			partial = true
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if partial {
		r.Error = firstErr.Error()
	}
	return r, partial
}

// importChunkedTable restores one data table in per-chunk transactions. The
// first chunk's transaction also wipes the table; each subsequent chunk commits
// independently. A chunk that fails is skipped and reported, and import
// continues with the next chunk. The progress callback is invoked after each
// successfully committed chunk with the cumulative inserted count for this
// table.
func (s *PostgresStore) importChunkedTable(ctx context.Context, sealer *Sealer, bt backupTable, rows []json.RawMessage, progress func(int)) (inserted int, skipped int, firstErr error) {
	wiped := false
	for start := 0; start < len(rows); start += importBatchSize {
		end := start + importBatchSize
		if end > len(rows) {
			end = len(rows)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("postgres store: import chunk begin %s: %w", bt.name, err)
			}
			skipped += end - start
			continue
		}
		// The wipe rides in the first successfully committed chunk. It is only
		// marked done once that chunk commits, so a first-chunk failure rolls the
		// wipe back and the next chunk retries it (the destination table keeps its
		// old rows until a chunk actually lands).
		if !wiped {
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+bt.name); err != nil {
				_ = tx.Rollback()
				if firstErr == nil {
					firstErr = fmt.Errorf("postgres store: import wipe %s: %w", bt.name, err)
				}
				skipped += end - start
				continue
			}
		}
		if err := s.insertRows(ctx, tx, sealer, bt, rows[start:end]); err != nil {
			_ = tx.Rollback()
			if firstErr == nil {
				firstErr = fmt.Errorf("postgres store: import chunk %s: %w", bt.name, err)
			}
			skipped += end - start
			continue
		}
		if err := tx.Commit(); err != nil {
			_ = tx.Rollback()
			if firstErr == nil {
				firstErr = fmt.Errorf("postgres store: import chunk commit %s: %w", bt.name, err)
			}
			skipped += end - start
			continue
		}
		wiped = true
		inserted += end - start
		if progress != nil {
			progress(inserted)
		}
	}
	return inserted, skipped, firstErr
}

// resyncTableSequence runs resyncIdSequence for one table in a short-lived
// transaction of its own. Data tables restore in per-chunk transactions, so
// there is no caller transaction to run it inside; a separate commit keeps the
// sequence resync durable once the last chunk has landed.
func (s *PostgresStore) resyncTableSequence(ctx context.Context, table string) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres store: resync begin %s: %w", table, err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	s.resyncIdSequence(ctx, tx, table)
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("postgres store: resync commit %s: %w", table, err)
	}
	return nil
}

// restorePricingSourceFiles materializes any embedded pricing-source catalog
// files into the destination pricing-sources directory and rewrites the file_path
// column of the corresponding rows to the new on-disk location.
func (s *PostgresStore) restorePricingSourceFiles(ctx context.Context, tx *sql.Tx, data *BackupResourceData) error {
	table := s.PricingSourcesTable()
	rows, ok := data.Tables[table]
	if !ok {
		return nil
	}
	for _, raw := range rows {
		var row struct {
			SourceType string `json:"source_type"`
			FilePath   string `json:"file_path"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			continue
		}
		if row.SourceType != "file" || row.FilePath == "" {
			continue
		}
		content, ok := data.Files[row.FilePath]
		if !ok {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return fmt.Errorf("postgres store: pricing source file decode: %w", err)
		}
		dest := filepath.Join(s.pricingSourcesDir, filepath.Base(row.FilePath))
		if err := os.WriteFile(dest, decoded, 0o644); err != nil {
			return fmt.Errorf("postgres store: pricing source file write: %w", err)
		}
		// Point the row at the destination file path.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(
			"UPDATE %s SET file_path = $1 WHERE id = $2", table), dest, row.FilePath); err != nil {
			return fmt.Errorf("postgres store: pricing source file_path update: %w", err)
		}
	}
	return nil
}

// defaultTableHint maps a fully-qualified table name back to its bare default
// name for sealer-column lookups. It strips any schema prefix and quotes.
func defaultTableHint(qualified string) string {
	name := strings.Trim(qualified, `"`)
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		name = name[idx+1:]
	}
	return name
}

// unquoteTable strips the schema prefix and surrounding quotes from a
// fully-qualified table name, yielding the bare unquoted table name.
func unquoteTable(qualified string) string {
	name := qualified
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		name = name[idx+1:]
	}
	name = strings.Trim(name, `"`)
	// A name may end up with a leading quote when the schema prefix and the
	// table name are quoted independently (e.g. "schema"."table"); strip it.
	name = strings.TrimPrefix(name, `"`)
	return name
}

// tableSchema extracts the schema part of a fully-qualified table name,
// falling back to the default schema when the name is unqualified.
func tableSchema(qualified string) string {
	if idx := strings.LastIndex(qualified, "."); idx >= 0 {
		return strings.Trim(qualified[:idx], `"`)
	}
	return "public"
}

// resyncIdSequence advances a table's serial-id sequence past the highest
// currently stored id. Import restores rows with their original explicit ids,
// which the sequence does not know about; without a resync the next
// auto-generated id can collide with a restored id and fail the insert. The
// function discovers the owning sequence via pg_get_serial_sequence and only
// touches tables that actually carry one (no-op otherwise). Tables without an
// id column (e.g. api_key_policies, keyed by api_key_id) are skipped without
// error — pg_get_serial_sequence raises SQLSTATE 42703 for a missing column,
// and a failed statement would abort the caller's import transaction.
func (s *PostgresStore) resyncIdSequence(ctx context.Context, tx *sql.Tx, table string) {
	var hasID bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = $1 AND table_name = $2 AND column_name = 'id')`,
		tableSchema(table), unquoteTable(table),
	).Scan(&hasID); err != nil {
		log.WithError(err).WithField("table", table).Debug("postgres store: import: id column lookup failed")
		return
	}
	if !hasID {
		return // no id column on this table
	}
	var seqName sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT pg_get_serial_sequence($1, 'id')`, table,
	).Scan(&seqName); err != nil {
		log.WithError(err).WithField("table", table).Debug("postgres store: import: sequence lookup failed")
		return
	}
	if !seqName.Valid || seqName.String == "" {
		return // no serial id column on this table
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		`SELECT setval(%s, COALESCE((SELECT MAX(id) + 1 FROM %s), 1), false)`,
		quoteLiteral(seqName.String), table,
	)); err != nil {
		// Best-effort: a failed resync leaves the sequence too low, which can
		// cause later auto-id collisions, so surface it rather than swallowing.
		log.WithError(err).WithField("table", table).Error("postgres store: import: id sequence resync failed")
	}
}

// quoteLiteral quotes a string as a SQL string literal (single quotes doubled),
// matching pg_get_serial_sequence's need for a text argument.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
