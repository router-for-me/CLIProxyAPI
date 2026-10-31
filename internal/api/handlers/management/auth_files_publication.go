package management

import (
	"context"
	"os"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/authpublication"
)

func removePublishedAuthFile(ctx context.Context, path string) error {
	return codex.WithFileLockContext(ctx, path, func() error {
		err := os.Remove(path)
		if !os.IsNotExist(err) {
			return err
		}
		// A confirmed remote commit is deletable even if its first mirror failed.
		// Leave the record active until the backing store confirms deletion.
		_, errRead := authpublication.ReadFile(path)
		return errRead
	})
}
