// Package runtimebridge owns the ephemeral compatibility bridge that turns the
// PG-first runtime_config snapshot into a real on-disk config.yaml the
// file-bound CLIProxyAPI core can consume. PostgreSQL remains the only
// source of truth; the bridge file is a generated artifact the existing
// fsnotify watcher reads.
package runtimebridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// snapshotRenderer is the contract Build relies on. The production
// implementation embeds *configsnapshot.Snapshot; tests provide a fake
// without depending on the real snapshot package to keep this package
// dependency-light.
type snapshotRenderer interface {
	// MarshalYAML returns deterministic YAML bytes suitable for the
	// file-watcher to consume. A non-nil error aborts the render.
	MarshalYAML() ([]byte, error)
	// Validate parses its own YAML output and returns an error when the
	// content is malformed. Build calls Validate after MarshalYAML so a
	// broken snapshot never lands on disk.
	Validate() error
	// AtomicWrite writes data to path atomically with the given mode.
	// Phase 2 reuses the central util/atomicfile helper, so this method
	// is a thin wrapper around the cross-package primitive.
	AtomicWrite(path string, data []byte, mode os.FileMode) error
}

const authsSubdir = "auths"

// Bridge owns one ephemeral directory tree. The directory is removed on
// Close; never reused across process restarts so stale state from a prior
// process cannot leak.
type Bridge struct {
	root       string // absolute path of the bridge root
	configPath string // absolute path of the generated config.yaml
	authDir    string // absolute path of the bridge auths/ subdir
	closeOnce  sync.Once
}

// Root returns the absolute path of the bridge directory.
func (b *Bridge) Root() string { return b.root }

// ConfigPath returns the absolute path of the generated config.yaml.
func (b *Bridge) ConfigPath() string { return b.configPath }

// AuthDir returns the absolute path of the bridge auths/ subdir.
func (b *Bridge) AuthDir() string { return b.authDir }

// Close removes the bridge directory tree. Safe to call multiple times;
// errors are swallowed because shutdown must not mask a more important
// error from the caller's service Run.
func (b *Bridge) Close() error {
	if b == nil {
		return nil
	}
	var firstErr error
	b.closeOnce.Do(func() {
		if b.root == "" {
			return
		}
		if err := os.RemoveAll(b.root); err != nil {
			firstErr = fmt.Errorf("runtimebridge: remove %s: %w", b.root, err)
		}
	})
	return firstErr
}

// baseDir picks the per-writable runtime directory, mirroring the
// fallback used by store.NewPostgresStore when no SpoolDir is configured.
func baseDir() (string, error) {
	// Phase 2 reuses the same writable-path resolution the store layer uses.
	// We avoid importing store to keep this package dependency-light, so we
	// mirror the relevant env contract inline. The contract is
	// WRITABLE_PATH; "" means fall back to os.TempDir().
	const writableEnv = "WRITABLE_PATH"
	if v := os.Getenv(writableEnv); v != "" {
		return v, nil
	}
	return os.TempDir(), nil
}

// Build creates a new ephemeral bridge directory and renders the initial
// config.yaml from snap. The file is written only after validation
// confirms the rendered bytes parse into a *config.Config, so a malformed
// snapshot never lands on disk.
func Build(ctx context.Context, snap snapshotRenderer, authDirHint string) (*Bridge, error) {
	if snap == nil {
		return nil, errors.New("runtimebridge: snapshot is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	base, err := baseDir()
	if err != nil {
		return nil, fmt.Errorf("runtimebridge: resolve base dir: %w", err)
	}
	if base == "" {
		return nil, errors.New("runtimebridge: base dir is empty")
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, fmt.Errorf("runtimebridge: mkdir %s: %w", base, err)
	}
	root, err := os.MkdirTemp(base, "nixllm-bridge-*")
	if err != nil {
		return nil, fmt.Errorf("runtimebridge: mkdir temp: %w", err)
	}
	authDir := filepath.Join(root, authsSubdir)
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("runtimebridge: mkdir auths: %w", err)
	}
	cfgPath := filepath.Join(root, "config.yaml")
	if err := renderTo(snap, cfgPath, 0o600); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("runtimebridge: render: %w", err)
	}
	_ = authDirHint // Phase 2 currently ignores the hint; the legacy store AuthDir keeps serving auths.
	return &Bridge{root: root, configPath: cfgPath, authDir: authDir}, nil
}

// renderTo renders the snapshot, validates it, and atomically writes
// config.yaml. Phase 2 delegates validation through the same
// configvalidation.Validate contract the runtime already uses, so the
// watcher will see a file the core is happy to consume.
func renderTo(snap snapshotRenderer, path string, mode os.FileMode) error {
	data, err := snap.MarshalYAML()
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	if err := snap.Validate(); err != nil {
		return fmt.Errorf("validate snapshot: %w", err)
	}
	return snap.AtomicWrite(path, data, mode)
}
