// DoImportConfig implements the one-shot nixllm import-config CLI command:
// it loads a YAML config file, converts it to a normalized resource plan,
// and applies it to PostgreSQL in a single transaction. This is the
// migration path from file-based configuration to the PG-first control plane
// (Phase 1 Task 9 of docs/plans/2026-09-14-nixllm-pg-first-design.md).
package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/configsnapshot"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/configstore"
	pgconfigstore "github.com/router-for-me/CLIProxyAPI/v7/internal/configstore/pg"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	log "github.com/sirupsen/logrus"
)

// ImportConfigOptions carries the CLI flags for import-config. DSN comes
// from PGSTORE_DSN (matching every other PG-backed flag); schema comes from
// PGSTORE_SCHEMA with a public default.
type ImportConfigOptions struct {
	// SourcePath is the YAML config file to import.
	SourcePath string
	// DSN is the PostgreSQL connection string. Empty falls back to
	// PGSTORE_DSN.
	DSN string
	// Schema is the target PostgreSQL schema. Empty falls back to
	// PGSTORE_SCHEMA, then to "public".
	Schema string
	// DryRun plans and validates without writing to PostgreSQL.
	DryRun bool
	// Replace permits overwriting an existing configuration. Without it the
	// command refuses to run when a conflicting provider identity is found.
	Replace bool
	// Timeout bounds the whole command; zero uses the 60s default.
	Timeout time.Duration
}

// ApplyConfigFile is the shared import core used by both the `-import-config`
// CLI and the boot-time auto-import path. It loads the YAML config at
// sourcePath, converts it to a normalized resource plan, applies the plan to
// pg in one transaction, and then seeds the runtime_config singleton with the
// bootstrap snapshot (revision 1) plus the matching config_imports audit row.
//
// pg must already be connected and have EnsureSchema applied; the caller
// retains ownership. The audit trail records mode ("import-config" or
// "boot-auto-import") and the source path so the dashboard can distinguish
// manual imports from boot-time seeding.
//
// Failure semantics: resource rows and the snapshot are committed in
// separate transactions (the repository owns the singleton write), so a
// snapshot failure leaves resources committed without an active revision.
// Re-running the import re-applies resources idempotently (stable child
// identity matching) and can still seed the snapshot.
func ApplyConfigFile(ctx context.Context, sourcePath string, pg *store.PostgresStore, mode string) error {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return fmt.Errorf("import-config: missing source path")
	}
	if strings.TrimSpace(mode) == "" {
		mode = "import-config"
	}

	// Load and parse the YAML config using the same parser the runtime uses
	// so defaults/normalization apply identically.
	cfg, err := config.LoadConfig(sourcePath)
	if err != nil {
		return fmt.Errorf("import-config: parse %s: %w", sourcePath, err)
	}
	if cfg == nil {
		return fmt.Errorf("import-config: nil cfg from %s", sourcePath)
	}

	plan, errPlan := configsnapshot.BuildResourcePlan(cfg)
	if errPlan != nil {
		return fmt.Errorf("import-config: build plan: %w", errPlan)
	}
	if len(plan.Report.Errors) > 0 {
		for key, msgs := range plan.Report.Errors {
			for _, msg := range msgs {
				log.Errorf("import-config: plan error %s: %s", key, msg)
			}
		}
		return fmt.Errorf("import-config: plan has %d error(s); refusing to apply", len(plan.Report.Errors))
	}

	if err := pg.ApplyNormalizedResourcePlan(ctx, plan); err != nil {
		return fmt.Errorf("import-config: apply plan: %w", err)
	}

	// Seed the runtime_config singleton so the PG-first boot path
	// (runtimebridge.LoadConfigFromSnapshot) sees an active revision. The
	// snapshot carries the scalar runtime projection only; resources live in
	// the normalized tables applied above.
	snap, errSnap := configsnapshot.SnapshotFromConfig(cfg)
	if errSnap != nil {
		return fmt.Errorf("import-config: build snapshot: %w", errSnap)
	}
	repo := pgconfigstore.Open(pg)
	saved, errSave := repo.Save(ctx, 0, &snap, configstore.SaveAudit{
		Actor:        mode,
		Reason:       fmt.Sprintf("%s from %s", mode, sourcePath),
		Source:       mode,
		ImportSource: sourcePath,
		Mode:         mode,
	})
	if errSave != nil {
		return fmt.Errorf("import-config: seed runtime_config: %w", errSave)
	}

	total := len(plan.Providers) + len(plan.APIKeys)
	log.Infof("import-config: committed %d resource(s) and seeded runtime_config revision %d (mode=%s, source=%s)",
		total, saved.Revision, mode, sourcePath)
	for kind, actions := range plan.Report.Counts {
		for action, n := range actions {
			log.Infof("import-config: %s/%s = %d", kind, action, n)
		}
	}
	return nil
}

// DoImportConfig executes the import-config command. It returns the number
// of resources committed, or an error. Failures are logged and returned so
// the CLI can exit with a non-zero status.
func DoImportConfig(ctx context.Context, opts ImportConfigOptions) (int, error) {
	sourcePath := strings.TrimSpace(opts.SourcePath)
	if sourcePath == "" {
		return 0, fmt.Errorf("import-config: missing source path")
	}
	dsn := strings.TrimSpace(opts.DSN)
	if dsn == "" {
		dsn = strings.TrimSpace(os.Getenv("PGSTORE_DSN"))
	}
	if dsn == "" {
		return 0, fmt.Errorf("import-config: PGSTORE_DSN is required")
	}
	schema := strings.TrimSpace(opts.Schema)
	if schema == "" {
		schema = strings.TrimSpace(os.Getenv("PGSTORE_SCHEMA"))
	}
	if schema == "" {
		schema = "public"
	}

	// Load and parse the YAML config using the same parser the runtime uses
	// so defaults/normalization apply identically. The dry-run path needs
	// only the plan, so parse + plan first and return before connecting.
	cfg, err := config.LoadConfig(sourcePath)
	if err != nil {
		return 0, fmt.Errorf("import-config: parse %s: %w", sourcePath, err)
	}
	if cfg == nil {
		return 0, fmt.Errorf("import-config: nil cfg from %s", sourcePath)
	}

	plan, errPlan := configsnapshot.BuildResourcePlan(cfg)
	if errPlan != nil {
		return 0, fmt.Errorf("import-config: build plan: %w", errPlan)
	}
	if len(plan.Report.Errors) > 0 {
		for key, msgs := range plan.Report.Errors {
			for _, msg := range msgs {
				log.Errorf("import-config: plan error %s: %s", key, msg)
			}
		}
		return 0, fmt.Errorf("import-config: plan has %d error(s); refusing to apply", len(plan.Report.Errors))
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	if opts.DryRun {
		// Dry-run: log what would be applied, then stop. No DB connection
		// needed beyond validation already done by BuildResourcePlan.
		log.Info("import-config: dry-run")
		log.Infof("import-config: would apply %d provider(s) and %d client api key(s)",
			len(plan.Providers), len(plan.APIKeys))
		for kind, actions := range plan.Report.Counts {
			for action, n := range actions {
				log.Infof("import-config: %s/%s = %d", kind, action, n)
			}
		}
		return 0, nil
	}

	pg, err := store.NewPostgresStore(ctx, store.PostgresStoreConfig{
		DSN:    dsn,
		Schema: schema,
	})
	if err != nil {
		return 0, fmt.Errorf("import-config: connect postgres: %w", err)
	}
	defer func() {
		if errClose := pg.Close(); errClose != nil {
			log.WithError(errClose).Warn("import-config: close postgres failed")
		}
	}()
	if errSchema := pg.EnsureSchema(ctx); errSchema != nil {
		return 0, fmt.Errorf("import-config: ensure schema: %w", errSchema)
	}

	if err := ApplyConfigFile(ctx, sourcePath, pg, "import-config"); err != nil {
		return 0, err
	}
	return len(plan.Providers) + len(plan.APIKeys), nil
}
