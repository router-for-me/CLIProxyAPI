package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// The destination lock excludes refresh writers until the remote commit and local
// publication finish. Uncommitted credentials never occupy a discoverable JSON path.
func persistAcquisitionMirror(ctx context.Context, path string, write func(string) error, persist func(string) error, published func()) error {
	if !cliproxyauth.HasAuthCreationIntent(ctx) {
		if errWrite := write(path); errWrite != nil {
			return errWrite
		}
		return persist(path)
	}
	return codex.WithFileLockContext(ctx, path, func() error {
		info, err := os.Lstat(path)
		existed := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("snapshot auth mirror: %w", err)
		}
		if existed && !info.Mode().IsRegular() {
			return fmt.Errorf("acquisition auth mirror must be a regular file")
		}
		var original []byte
		if existed {
			original, err = os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("snapshot auth mirror: %w", err)
			}
		}
		dir, err := os.MkdirTemp(filepath.Dir(path), ".auth-acquisition-*")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(dir) }()
		stagedPath := filepath.Join(dir, "credential")
		if existed {
			if err := os.WriteFile(stagedPath, original, 0o600); err != nil {
				return err
			}
		}
		if err := write(stagedPath); err != nil {
			return err
		}
		stagedInfo, err := os.Lstat(stagedPath)
		if err != nil {
			return fmt.Errorf("inspect acquired credential: %w", err)
		}
		if !stagedInfo.Mode().IsRegular() {
			return fmt.Errorf("acquired credential must be a regular file")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := persist(stagedPath); err != nil {
			return err
		}
		// The remote commit is authoritative. Reconcile the mirror without turning a
		// saved login into an OAuth failure if local publication is unavailable.
		if err := os.Rename(stagedPath, path); err != nil {
			log.Warnf("auth store: remote credential commit succeeded but local publication failed: %v", err)
		} else if published != nil {
			published()
		}
		return nil
	})
}
