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
	// so defaults/normalization apply identically.
	cfg, err := config.LoadConfig(sourcePath)
	if err != nil {
		return 0, fmt.Errorf("import-config: parse %s: %w", sourcePath, err)
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

	if err := pg.ApplyNormalizedResourcePlan(ctx, plan); err != nil {
		return 0, fmt.Errorf("import-config: apply plan: %w", err)
	}

	total := len(plan.Providers) + len(plan.APIKeys)
	log.Infof("import-config: committed %d resource(s) to schema %q", total, schema)
	for kind, actions := range plan.Report.Counts {
		for action, n := range actions {
			log.Infof("import-config: %s/%s = %d", kind, action, n)
		}
	}
	return total, nil
}
