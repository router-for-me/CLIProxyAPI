package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/authpublication"
)

// A process can stop after preparing the reader barrier but before creating the
// Git journal. No credential commit can precede that journal.
func discardUnjournaledPublicationBarriers(ctx context.Context, repoDir, authDir string) error {
	paths, err := authpublication.Paths(authDir)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err := codex.WithFileLockContext(ctx, path, func() error {
			rel, err := filepath.Rel(repoDir, path)
			if err != nil {
				return err
			}
			if _, err := os.Stat(gitPublicationPath(repoDir, rel)); !errors.Is(err, os.ErrNotExist) {
				return err
			}
			return authpublication.DiscardUnconfirmed(path)
		}); err != nil {
			return err
		}
	}
	return nil
}
