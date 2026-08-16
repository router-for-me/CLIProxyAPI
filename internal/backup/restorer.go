package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// RestoreMode selects the behavior of a full restore for data already present.
type RestoreMode string

const (
	// RestoreModeReplace wipes and replaces existing data to mirror the snapshot.
	RestoreModeReplace RestoreMode = "replace"
	// RestoreModeMerge adds/updates from the snapshot without deleting existing data.
	RestoreModeMerge RestoreMode = "merge"
)

// ValidRestoreMode reports whether m is a known restore mode.
func ValidRestoreMode(m RestoreMode) bool { return m == RestoreModeReplace || m == RestoreModeMerge }

// RestoreResult summarizes a Restore call: the optional PG report plus the
// number of files (config + auth) written.
type RestoreResult struct {
	Report *store.BackupImportReport `json:"report,omitempty"`
	Files  int                       `json:"files"`
}

// pgImporter is the subset of *store.PostgresStore needed by Restorer.
type pgImporter interface {
	ImportData(ctx context.Context, bundle store.BackupBundle, opts store.BackupImportOpts) (store.BackupImportReport, error)
}

// fileWriter persists config.yaml/auths/ changes back to the active store so
// the watcher's persistence path mirrors them outward.
type fileWriter interface {
	PersistConfig(ctx context.Context) error
	PersistAuthFiles(ctx context.Context, message string, paths ...string) error
}

// Restorer restores a full backup bundle to the active instance.
type Restorer struct {
	importer  pgImporter
	persister fileWriter
	cfgPath   string
	authDir   string
}

// NewRestorer builds a Restorer. importer may be nil (non-PG deployment) — only
// the file portions of the bundle are applied in that case. The caller is
// expected to validate the restore mode before calling Restore.
func NewRestorer(importer pgImporter, persister fileWriter, cfgPath, authDir string) *Restorer {
	return &Restorer{importer: importer, persister: persister, cfgPath: cfgPath, authDir: authDir}
}

// mergeImportResources returns the data resources present in the bundle. Merge
// restores data only: ImportData wipes every resource it touches, and wiping
// config tables would not be "merge".
func mergeImportResources(bundle store.BackupBundle) []store.BackupResource {
	var out []store.BackupResource
	for _, r := range store.AllBackupResources {
		if !store.IsBackupDataResource(r) {
			continue
		}
		if _, ok := bundle.Resources[string(r)]; ok {
			out = append(out, r)
		}
	}
	return out
}

// Restore applies bundle to the live instance according to mode. PG tables are
// restored via the store's ImportData (skipped when no importer); config.yaml
// and auths/ files are written per mode (replace overwrites, merge fills gaps).
func (r *Restorer) Restore(ctx context.Context, bundle store.BackupBundle, mode RestoreMode) (RestoreResult, error) {
	runnerMu.Lock()
	defer runnerMu.Unlock()

	var result RestoreResult
	if r.importer != nil {
		opts := store.BackupImportOpts{}
		if mode == RestoreModeMerge {
			opts.Resources = mergeImportResources(bundle)
		}
		report, err := r.importer.ImportData(ctx, bundle, opts)
		if err != nil {
			return result, err
		}
		result.Report = &report
	}

	written, err := r.writeConfig(bundle, mode)
	if err != nil {
		return result, err
	}
	result.Files += written

	authWritten, err := r.writeAuthFiles(bundle, mode)
	if err != nil {
		return result, err
	}
	result.Files += len(authWritten)

	if len(authWritten) > 0 && r.persister != nil {
		if err := r.persister.PersistAuthFiles(ctx, "backup restore", authWritten...); err != nil {
			return result, err
		}
	}
	return result, nil
}

// writeConfig writes bundle.ConfigYAML to cfgPath following mode; returns the
// number of files written (0 or 1).
func (r *Restorer) writeConfig(bundle store.BackupBundle, mode RestoreMode) (int, error) {
	if r.cfgPath == "" || bundle.ConfigYAML == "" {
		return 0, nil
	}
	if mode == RestoreModeMerge {
		if _, err := os.Stat(r.cfgPath); err == nil {
			return 0, nil
		} else if !os.IsNotExist(err) {
			return 0, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(r.cfgPath), 0o700); err != nil {
		return 0, err
	}
	if err := os.WriteFile(r.cfgPath, []byte(bundle.ConfigYAML), 0o600); err != nil {
		return 0, err
	}
	return 1, nil
}

// writeAuthFiles writes bundle.AuthFiles under authDir following mode and
// returns the list of paths written. Paths are rejected when they escape
// authDir (absolute or containing traversal).
func (r *Restorer) writeAuthFiles(bundle store.BackupBundle, mode RestoreMode) ([]string, error) {
	if len(bundle.AuthFiles) == 0 || r.authDir == "" {
		return nil, nil
	}
	root := filepath.Clean(filepath.FromSlash(r.authDir))
	var written []string
	for _, f := range bundle.AuthFiles {
		cleanRel := filepath.Clean(filepath.FromSlash(f.Path))
		if filepath.IsAbs(cleanRel) || cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) {
			return written, fmt.Errorf("backup restore: auth file path %q escapes the auth directory", f.Path)
		}
		dest := filepath.Join(root, cleanRel)
		if mode == RestoreModeMerge {
			if _, err := os.Stat(dest); err == nil {
				continue
			} else if !os.IsNotExist(err) {
				return written, err
			}
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return written, err
		}
		if err := os.WriteFile(dest, []byte(f.Content), 0o600); err != nil {
			return written, err
		}
		written = append(written, dest)
	}
	return written, nil
}
