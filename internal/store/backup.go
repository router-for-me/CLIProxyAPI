package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
)

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
	ResourceManagementTokens,
	ResourceErrorMessages,
	ResourcePricingSources,
	ResourceAuthFiles,
	ResourceUsage,
	ResourceAlerts,
	ResourceModelHealth,
	ResourceSyncLog,
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
	default:
		return nil
	}
}

// BackupBundle is the portable JSON document produced by ExportData and consumed
// by ImportData. Resources maps a resource key to a per-table set of rows.
// Summary carries a per-resource total row count so the frontend can render a
// pre-import preview without parsing every row.
type BackupBundle struct {
	Version    int                           `json:"version"`
	ExportedAt time.Time                     `json:"exported_at"`
	Summary    map[string]int                `json:"summary,omitempty"`
	Resources  map[string]backupResourceData `json:"resources"`
}

// ResourceRowCounts returns the per-resource total exported row counts from the
// bundle's header summary, used by handlers to preview a bundle before import.
// For bundles exported before the summary header existed, it falls back to
// deriving the counts from the exported rows themselves.
func (b BackupBundle) ResourceRowCounts() map[string]int {
	if len(b.Summary) > 0 {
		return b.Summary
	}
	out := make(map[string]int, len(b.Resources))
	for key, data := range b.Resources {
		out[key] = rowCount(data)
	}
	return out
}

// rowCount sums the exported rows across all tables of one resource data set.
func rowCount(data backupResourceData) int {
	n := 0
	for _, rows := range data.Tables {
		n += len(rows)
	}
	return n
}

// backupResourceData holds the exported rows for one resource. Tables maps a
// table name to its exported rows; Files carries base64-encoded on-disk
// payloads (currently only pricing-source catalog files) keyed by the absolute
// path recorded in the corresponding table row.
type backupResourceData struct {
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
}

// BackupImportReport summarizes the per-resource outcome of ImportData.
type BackupImportReport struct {
	Resources map[BackupResource]BackupImportResourceReport `json:"resources,omitempty"`
	Total     int                                           `json:"total_inserted"`
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

// importTable wipes and restores one table from its exported rows, applying the
// seal transform to sealed columns. Rows are restored via jsonb_populate_record
// so column types are preserved. Deleting then re-inserting happens within the
// caller's transaction.
func (s *PostgresStore) importTable(ctx context.Context, tx *sql.Tx, sealer *Sealer, bt backupTable, rows []json.RawMessage) (int, int, error) {
	cols := sealedColumnsByTable[defaultTableHint(bt.name)]
	if _, err := tx.ExecContext(ctx, "DELETE FROM "+bt.name); err != nil {
		return 0, 0, fmt.Errorf("postgres store: import wipe %s: %w", bt.name, err)
	}
	inserted, skipped := 0, 0
	for _, raw := range rows {
		final := raw
		if len(cols) > 0 {
			transformed, err := transformSealedColumns(sealer, raw, cols, false)
			if err != nil {
				return inserted, skipped, fmt.Errorf("postgres store: import %s seal: %w", bt.name, err)
			}
			final = transformed
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(
			"INSERT INTO %s SELECT * FROM jsonb_populate_record(NULL::%s, $1::jsonb)", bt.name, bt.name),
			string(final),
		); err != nil {
			// A single malformed row should not abort the whole import; skip
			// it and record the failure so the operator can investigate.
			log.WithError(err).WithField("table", bt.name).Debug("postgres store: import: skipping row")
			skipped++
			continue
		}
		inserted++
	}
	return inserted, skipped, nil
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

// ExportData produces a portable BackupBundle for the requested resources
// (default all). Within each resource, tables are ranked parents-first
// (already reflected in resourceTables). Sealed columns are unsealed so the
// bundle is portable across instances regardless of encryption keys.
func (s *PostgresStore) ExportData(ctx context.Context, opts BackupExportOpts) (BackupBundle, error) {
	bundle := BackupBundle{
		Version:    1,
		ExportedAt: time.Now().UTC(),
		Summary:    make(map[string]int),
		Resources:  make(map[string]backupResourceData),
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
		data := backupResourceData{Tables: make(map[string][]json.RawMessage), Files: make(map[string]string)}
		for _, bt := range s.resourceTables(res) {
			rows, err := s.exportTable(ctx, sealer, bt)
			if err != nil {
				return bundle, err
			}
			if len(rows) > 0 {
				data.Tables[bt.name] = rows
			}
		}
		// Pricing-source catalog files live on disk, not in the DB. Embed their
		// content so a file-backed catalog survives the round-trip.
		if res == ResourcePricingSources {
			if err := s.embedPricingSourceFiles(ctx, &data); err != nil {
				return bundle, err
			}
		}
		bundle.Resources[string(res)] = data
		bundle.Summary[string(res)] = rowCount(data)
	}
	return bundle, nil
}

// embedPricingSourceFiles reads each source_type=file pricing source's on-disk
// catalog and stores it base64-encoded in the resource data, keyed by the
// absolute file_path recorded in the row.
func (s *PostgresStore) embedPricingSourceFiles(ctx context.Context, data *backupResourceData) error {
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
// BackupBundle. Selected resources are wiped then re-inserted within a single
// transaction; on any hard failure the whole import rolls back. Insert failures
// for individual rows are skipped and reported rather than aborting the batch.
func (s *PostgresStore) ImportData(ctx context.Context, bundle BackupBundle, opts BackupImportOpts) (BackupImportReport, error) {
	report := BackupImportReport{Resources: make(map[BackupResource]BackupImportResourceReport)}
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

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return report, fmt.Errorf("postgres store: import begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Wipe selected resources' tables first, in reverse resource order and
	// reverse table order (children before parents), so FK-parents are never
	// deleted before their referencing children.
	for i := len(resources) - 1; i >= 0; i-- {
		res := resources[i]
		data, ok := bundle.Resources[string(res)]
		if !ok {
			continue
		}
		tables := s.resourceTables(res)
		for j := len(tables) - 1; j >= 0; j-- {
			if _, ok := data.Tables[tables[j].name]; !ok {
				continue
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+tables[j].name); err != nil {
				return report, fmt.Errorf("postgres store: import wipe %s: %w", tables[j].name, err)
			}
		}
	}

	// Insert parents before children, resource order as defined.
	for _, res := range resources {
		data, ok := bundle.Resources[string(res)]
		if !ok {
			continue
		}
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
				r.Error = err.Error()
				report.Resources[res] = r
				return report, err
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
			if err := s.restorePricingSourceFiles(ctx, tx, &data); err != nil {
				r.Error = err.Error()
				report.Resources[res] = r
				return report, err
			}
		}
		report.Resources[res] = r
		report.Total += r.Inserted
	}

	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("postgres store: import commit: %w", err)
	}
	return report, nil
}

// restorePricingSourceFiles materializes any embedded pricing-source catalog
// files into the destination pricing-sources directory and rewrites the file_path
// column of the corresponding rows to the new on-disk location.
func (s *PostgresStore) restorePricingSourceFiles(ctx context.Context, tx *sql.Tx, data *backupResourceData) error {
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
