package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
)

var codexFileLocks sync.Map

func lockForFilePath(authFilePath string) *misc.ContextMutex {
	cleaned := canonicalLockPath(authFilePath)
	if actual, ok := codexFileLocks.Load(cleaned); ok {
		return actual.(*misc.ContextMutex)
	}
	actual, _ := codexFileLocks.LoadOrStore(cleaned, &misc.ContextMutex{})
	return actual.(*misc.ContextMutex)
}

// WithFileLock serializes credential updates within and across processes.
func WithFileLock(authFilePath string, fn func() error) error {
	return WithFileLockContext(context.Background(), authFilePath, fn)
}

// WithFileLockContext lets a canceled credential save leave both lock queues
// without scheduling a write after the caller has already reported failure.
func WithFileLockContext(ctx context.Context, authFilePath string, fn func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	mu := lockForFilePath(authFilePath)
	if err := mu.LockContext(ctx); err != nil {
		return err
	}
	defer mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(authFilePath), 0o700); err != nil {
		return fmt.Errorf("failed to create credential directory: %w", err)
	}
	release, err := acquireInterProcessLockFileContext(ctx, authFilePath)
	if err != nil {
		return err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

func acquireInterProcessLockFile(authFilePath string) (func(), error) {
	return acquireInterProcessLockFileContext(context.Background(), authFilePath)
}

// OS lock waits cannot select on ctx.Done, so retry nonblocking attempts instead.
func waitForFileLock(ctx context.Context, tryLock func() (bool, error)) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		locked, err := tryLock()
		if err != nil {
			return err
		}
		if locked {
			return nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
