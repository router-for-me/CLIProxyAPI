package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/authpublication"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// The destination lock excludes refresh writers until the remote commit and local
// publication finish. Uncommitted credentials never occupy a discoverable JSON path.
func persistAcquisitionMirror(ctx context.Context, path string, auth *cliproxyauth.Auth, write func(string) error, persist func(string) error, published func()) error {
	auth.BindCommittedAuthFile("", nil)
	if !cliproxyauth.HasAuthCreationIntent(ctx) {
		if err := codex.WithFileLockContext(ctx, path, func() error { return authpublication.Repair(path) }); err != nil {
			return err
		}
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
			original, err = authpublication.ReadFile(path)
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
		data, err := os.ReadFile(stagedPath)
		if err != nil {
			return fmt.Errorf("read acquired credential: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		pending, err := authpublication.Prepare(path, data)
		if err != nil {
			return err
		}
		if err := persist(stagedPath); err != nil {
			return errors.Join(err, pending.Abort())
		}
		if err := pending.Commit(); err != nil {
			log.Warnf("auth store: remote credential committed but publication confirmation failed: %v", err)
		}
		// The remote commit is authoritative. Reconcile the mirror without turning a
		// saved login into an OAuth failure if local publication is unavailable.
		if err := os.Rename(stagedPath, path); err != nil {
			auth.BindCommittedAuthFile(path, data)
			log.Warnf("auth store: remote credential commit succeeded but local publication failed: %v", err)
		} else {
			if err := authpublication.Clear(path); err != nil {
				auth.BindCommittedAuthFile(path, data)
				log.Warnf("auth store: could not clear publication overlay: %v", err)
				return nil
			}
			if published != nil {
				published()
			}
		}
		return nil
	})
}
