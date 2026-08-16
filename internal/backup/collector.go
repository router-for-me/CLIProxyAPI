package backup

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	log "github.com/sirupsen/logrus"
)

// pgExporter is the subset of *store.PostgresStore needed by Collector.
// Kept as an unexported interface so tests can stub ExportData without a
// live Postgres.
type pgExporter interface {
	ExportData(ctx context.Context, opts store.BackupExportOpts) (store.BackupBundle, error)
}

// fileSource abstracts reading config.yaml and the auths/ directory so tests
// can point Collector at temp directories. The Collector only consumes the
// two paths, so callers can supply any small adapter over their runtime
// configuration.
type fileSource interface {
	ConfigPath() string
	AuthDir() string
}

// Collector gathers the full server state (PG resources + config.yaml +
// auths/) into a store.BackupBundle v2 with Mode "full".
type Collector struct {
	exporter pgExporter
	cfgPath  string
	authDir  string
}

// NewCollector builds a Collector. pg may be nil (non-PG deployment) — the
// collector still produces config_yaml and auth_files. src may be nil (paths
// not yet known) — Collect returns empty strings/slices for those.
func NewCollector(pg pgExporter, src fileSource) *Collector {
	c := &Collector{exporter: pg}
	if src != nil {
		c.cfgPath = src.ConfigPath()
		c.authDir = src.AuthDir()
	}
	return c
}

// Collect assembles the full backup bundle. When a PG exporter is wired it
// produces Resources from ExportData; when not, Resources is empty. config_yaml
// and auth_files are populated from the file paths given at construction.
func (c *Collector) Collect(ctx context.Context) (store.BackupBundle, error) {
	var bundle store.BackupBundle
	if c.exporter != nil {
		b, err := c.exporter.ExportData(ctx, store.BackupExportOpts{})
		if err != nil {
			return bundle, fmt.Errorf("backup collector: export PG data: %w", err)
		}
		bundle = b
	}
	bundle.Version = 2
	bundle.Mode = "full"

	// Read config.yaml. A missing file is not an error — non-PG deployments
	// may not have one yet — other read failures abort the backup.
	cfg, err := os.ReadFile(c.cfgPath)
	switch {
	case err == nil:
		bundle.ConfigYAML = string(cfg)
	case os.IsNotExist(err):
		// leave empty
	default:
		return bundle, fmt.Errorf("backup collector: read config: %w", err)
	}

	auths, err := c.collectAuthFiles()
	if err != nil {
		return bundle, err
	}
	bundle.AuthFiles = auths
	if bundle.Summary == nil {
		bundle.Summary = make(map[string]int)
	}
	if bundle.ConfigYAML != "" {
		bundle.Summary["config"] = 1
	}
	bundle.Summary["auth_files"] = len(auths)
	return bundle, nil
}

// collectAuthFiles walks the auth directory and returns one BackupAuthFile per
// regular file, keyed by slash-separated path relative to the auth dir. A
// missing or empty auth dir yields an empty slice without error. Unreadable
// entries are logged and skipped (best-effort export).
func (c *Collector) collectAuthFiles() ([]store.BackupAuthFile, error) {
	if c.authDir == "" {
		return nil, nil
	}
	if _, err := os.Stat(c.authDir); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("backup collector: stat auth dir: %w", err)
	}
	var files []store.BackupAuthFile
	errWalk := filepath.WalkDir(c.authDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, errRel := filepath.Rel(c.authDir, path)
		if errRel != nil {
			return errRel
		}
		rel = filepath.ToSlash(rel)
		content, errRead := os.ReadFile(path)
		if errRead != nil {
			log.WithField("path", rel).Warn("backup collector: skipping unreadable auth file")
			return nil
		}
		files = append(files, store.BackupAuthFile{Path: rel, Content: string(content)})
		return nil
	})
	if errWalk != nil {
		return nil, fmt.Errorf("backup collector: walk auth dir: %w", errWalk)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// pathSource adapts plain filesystem paths to fileSource.
type pathSource struct {
	cfgPath string
	authDir string
}

// ConfigPath returns the config.yaml path.
func (s pathSource) ConfigPath() string { return s.cfgPath }

// AuthDir returns the auths/ directory path.
func (s pathSource) AuthDir() string { return s.authDir }

// NewPathSource returns a fileSource backed by explicit filesystem paths. Use
// it when wiring the Collector from server configuration rather than in tests.
func NewPathSource(cfgPath, authDir string) fileSource {
	return pathSource{cfgPath: cfgPath, authDir: authDir}
}
