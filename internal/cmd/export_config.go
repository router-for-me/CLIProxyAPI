// DoExportConfig implements the one-shot nixllm export-config CLI command:
// it serialises the active runtime_config snapshot to a deterministic YAML
// file. Secrets are redacted by default so the produced artifact is safe to
// share as a migration bundle or a debug snapshot; pass IncludeSecrets to
// emit plaintext values for backup/restore use only.
//
// The exporter is read-only and never reloads the runtime. Atomic write
// (temp + rename) keeps the output consistent if the process is killed
// mid-run.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/configsnapshot"
	pgconfigstore "github.com/router-for-me/CLIProxyAPI/v7/internal/configstore/pg"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	log "github.com/sirupsen/logrus"
)

// ExportConfigOptions carries the CLI flags for export-config. DSN comes
// from PGSTORE_DSN; schema from PGSTORE_SCHEMA with a public default.
type ExportConfigOptions struct {
	DSN            string
	Schema         string
	OutputPath     string
	IncludeSecrets bool
}

// DoExportConfig executes the export-config command. Returns nil on success
// and a descriptive error otherwise. Errors are logged and returned so the
// CLI can exit with a non-zero status.
func DoExportConfig(ctx context.Context, opts ExportConfigOptions) error {
	dsn := strings.TrimSpace(opts.DSN)
	if dsn == "" {
		dsn = strings.TrimSpace(os.Getenv("PGSTORE_DSN"))
	}
	if dsn == "" {
		return fmt.Errorf("export-config: PGSTORE_DSN is required")
	}
	schema := strings.TrimSpace(opts.Schema)
	if schema == "" {
		schema = strings.TrimSpace(os.Getenv("PGSTORE_SCHEMA"))
	}
	if schema == "" {
		schema = "public"
	}
	outputPath := strings.TrimSpace(opts.OutputPath)
	if outputPath == "" {
		return fmt.Errorf("export-config: output path is required")
	}

	pg, err := store.NewPostgresStore(ctx, store.PostgresStoreConfig{
		DSN:    dsn,
		Schema: schema,
	})
	if err != nil {
		return fmt.Errorf("export-config: connect postgres: %w", err)
	}
	defer func() {
		if errClose := pg.Close(); errClose != nil {
			log.WithError(errClose).Warn("export-config: close postgres failed")
		}
	}()
	if errSchema := pg.EnsureSchema(ctx); errSchema != nil {
		return fmt.Errorf("export-config: ensure schema: %w", errSchema)
	}

	repo := pgconfigstore.Open(pg)
	snap, err := repo.Load(ctx)
	if err != nil {
		return fmt.Errorf("export-config: load snapshot: %w", err)
	}
	if snap == nil || snap.Revision == 0 {
		return errors.New("export-config: runtime_config has no active configuration")
	}

	raw, errMarshal := configsnapshot.MarshalYAML(snap)
	if errMarshal != nil {
		return fmt.Errorf("export-config: marshal snapshot: %w", errMarshal)
	}
	if !opts.IncludeSecrets {
		raw = redactSecrets(raw)
	}

	mode := os.FileMode(0o600)
	if !opts.IncludeSecrets {
		// 0o644 keeps the file readable for sharing without granting write
		// access to other users; secrets stay 0600.
		mode = 0o644
	}
	if err := atomicWriteFile(outputPath, raw, mode); err != nil {
		return fmt.Errorf("export-config: write %s: %w", outputPath, err)
	}

	log.Infof("export-config: wrote %s (revision=%d, secrets_redacted=%t)",
		outputPath, snap.Revision, !opts.IncludeSecrets)
	return nil
}

// redactSecrets replaces any in-line scalar value of a known secret field
// with "<redacted>" so a shareable export cannot leak credentials. The
// matcher is line-based and intentionally narrow: it handles the
// remote-management.secret-key field and the bare "api-key:" key that some
// provider entries expose. Future expansions belong in this function so
// every caller benefits.
func redactSecrets(b []byte) []byte {
	s := string(b)
	for _, k := range []string{"secret-key", "api-key"} {
		s = redactScalar(s, k)
	}
	return []byte(s)
}

func redactScalar(s, key string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, key+":") {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
		lines[i] = indent + key + ": <redacted>"
	}
	return strings.Join(lines, "\n")
}

// atomicWriteFile writes data to path via CreateTemp + rename so a partial
// file never replaces an existing artifact. Mode controls the final
// permissions on the renamed target.
func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".export-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
