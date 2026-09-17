package runtimebridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/configvalidation"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util/atomicfile"
	"gopkg.in/yaml.v3"
)

// BridgeOp is the minimal contract the cmd package consumes. Production
// code constructs a Bridge and passes it via BridgeOp; the test package
// can supply a fake without depending on the snapshot package.
type BridgeOp interface {
	ConfigPath() string
	AuthDir() string
	Close() error
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
	const writableEnv = "WRITABLE_PATH"
	if v := os.Getenv(writableEnv); v != "" {
		return v, nil
	}
	return os.TempDir(), nil
}

// Build creates a new ephemeral bridge directory and renders the initial
// config.yaml from snap (the SnapshotRenderer contract). Production callers
// that already hold a *config.Config should prefer BuildFromConfig.
func Build(ctx context.Context, snap SnapshotRenderer, authDirHint string) (*Bridge, error) {
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

// BuildFromConfig creates a new ephemeral bridge directory and renders the
// initial config.yaml from cfg (a parsed *config.Config). The file is
// written only after yaml.Marshal + configvalidation.ValidateParsed succeed
// so a malformed configuration never lands on disk.
func BuildFromConfig(ctx context.Context, cfg *config.Config, authDirHint string) (*Bridge, error) {
	if cfg == nil {
		return nil, errors.New("runtimebridge: cfg is nil")
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
	if err := renderConfigTo(cfg, cfgPath, 0o600); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("runtimebridge: render: %w", err)
	}
	_ = authDirHint // Phase 2 currently ignores the hint; the legacy store AuthDir keeps serving auths.
	return &Bridge{root: root, configPath: cfgPath, authDir: authDir}, nil
}

// renderConfigTo marshals cfg to YAML, validates via configvalidation, and
// atomically writes it to path.
func renderConfigTo(cfg *config.Config, path string, mode os.FileMode) error {
	if cfg == nil {
		return errors.New("runtimebridge: nil cfg")
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := configvalidation.ValidateParsed(cfg); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}
	return atomicfile.Write(path, data, mode)
}
