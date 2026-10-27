package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// The destination lock excludes refresh writers until the remote commit and local
// publication finish. Uncommitted credentials never occupy a discoverable JSON path.
func persistAcquisitionMirror(ctx context.Context, path string, write func(string) error, persist func(string) error) error {
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
		// Preserve an independent writer that does not participate in the file lock.
		currentInfo, err := os.Lstat(path)
		if existed {
			if err != nil || !os.SameFile(info, currentInfo) {
				return fmt.Errorf("auth mirror changed during acquisition")
			}
			current, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !bytes.Equal(current, original) {
				return fmt.Errorf("auth mirror changed during acquisition")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("auth mirror appeared during acquisition")
		}
		return os.Rename(stagedPath, path)
	})
}
