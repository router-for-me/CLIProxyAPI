// DoVerifyConfig implements the one-shot nixllm verify-config CLI command:
// it loads the active runtime_config snapshot from PostgreSQL, validates
// it round-trips through the shared validation pipeline, and confirms the
// active revision matches the latest config_revisions row. Exits 0 when
// every check passes; non-zero with a descriptive error otherwise.
//
// This is the same script operator might run after an import / dashboard
// edit to confirm the persisted singleton is internally consistent. It
// never mutates the database and never reloads the runtime.
package cmd

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	pgconfigstore "github.com/router-for-me/CLIProxyAPI/v7/internal/configstore/pg"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/configvalidation"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	log "github.com/sirupsen/logrus"
)

// VerifyConfigOptions carries the CLI flags for verify-config. DSN comes
// from PGSTORE_DSN; schema from PGSTORE_SCHEMA with a public default.
type VerifyConfigOptions struct {
	DSN    string
	Schema string
}

// DoVerifyConfig executes the verify-config command. Returns nil on success
// and an error describing the first failure otherwise. Errors are logged
// via logrus and returned so the CLI can exit with a non-zero status.
func DoVerifyConfig(ctx context.Context, opts VerifyConfigOptions) error {
	dsn := strings.TrimSpace(opts.DSN)
	if dsn == "" {
		dsn = strings.TrimSpace(os.Getenv("PGSTORE_DSN"))
	}
	if dsn == "" {
		return fmt.Errorf("verify-config: PGSTORE_DSN is required")
	}
	schema := strings.TrimSpace(opts.Schema)
	if schema == "" {
		schema = strings.TrimSpace(os.Getenv("PGSTORE_SCHEMA"))
	}
	if schema == "" {
		schema = "public"
	}

	pg, err := store.NewPostgresStore(ctx, store.PostgresStoreConfig{
		DSN:    dsn,
		Schema: schema,
	})
	if err != nil {
		return fmt.Errorf("verify-config: connect postgres: %w", err)
	}
	defer func() {
		if errClose := pg.Close(); errClose != nil {
			log.WithError(errClose).Warn("verify-config: close postgres failed")
		}
	}()
	if errSchema := pg.EnsureSchema(ctx); errSchema != nil {
		return fmt.Errorf("verify-config: ensure schema: %w", errSchema)
	}

	repo := pgconfigstore.Open(pg)
	snap, err := repo.Load(ctx)
	if err != nil {
		return fmt.Errorf("verify-config: load snapshot: %w", err)
	}
	if snap == nil || snap.Revision == 0 {
		return errors.New("verify-config: no active runtime configuration")
	}

	// Round-trip the snapshot through the canonical validation pipeline so
	// a corrupt Settings/Extra map (one the config parser would reject) is
	// surfaced as a verify failure.
	if _, errValidate := configvalidation.Validate(snap); errValidate != nil {
		return fmt.Errorf("verify-config: validate snapshot at revision %d: %w", snap.Revision, errValidate)
	}

	// Active revision in runtime_config must equal the latest row in
	// config_revisions. A mismatch means a writer bypassed the repository
	// contract (or restored from a partial backup).
	var latestRevision sql.NullInt64
	if errMax := pg.DB().QueryRowContext(ctx,
		fmt.Sprintf("SELECT MAX(revision) FROM %s", pg.ConfigRevisionsTable()),
	).Scan(&latestRevision); errMax != nil {
		return fmt.Errorf("verify-config: query max revision: %w", errMax)
	}
	if !latestRevision.Valid || latestRevision.Int64 != snap.Revision {
		return fmt.Errorf("verify-config: runtime_config revision %d does not match config_revisions max %v",
			snap.Revision, latestRevision)
	}

	log.Infof("verify-config: ok (revision=%d, schema=%q)", snap.Revision, schema)
	return nil
}
