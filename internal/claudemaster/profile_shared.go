package claudemaster

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"golang.org/x/sys/unix"
)

// Several claude-master processes (one per Claude Code window) share one profile. A subscription
// login rotates on every refresh, and the previous access token stops working the moment it does,
// so the processes must agree on one current credential:
//
//   - the credential FILE is the single source of truth; every write is an atomic rename;
//   - a refresh (the only rotation) happens under an exclusive flock beside the auth directory, and
//     first re-reads the file: if another process already rotated, it ADOPTS that credential and
//     calls Anthropic not at all;
//   - every request notices a newer file (one stat) and uses it, and a 401 adopts or refreshes
//     under the same lock before one retry;
//   - Save never writes older tokens over newer ones.
//
// The lock is released by the kernel when its holder dies, so a crash cannot wedge the others.
const refreshLockWait = 2 * time.Minute

// credentialTokenKeys are the fields one rotation replaces together.
var credentialTokenKeys = []string{"access_token", "refresh_token", "expired", "last_refresh", "id_token"}

// lockRefresh takes the profile's cross-process refresh lock. It lives beside the auth directory
// (the same convention as the .routes directory), so nothing is added to the directory that must
// hold exactly one credential.
func lockRefresh(ctx context.Context, authDir string) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	fd, err := unix.Open(filepath.Clean(authDir)+".refresh.lock", unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, errors.New("cannot open the credential refresh lock")
	}
	f := os.NewFile(uintptr(fd), "refresh.lock")
	if err := privateFile(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	timeout := time.NewTimer(refreshLockWait)
	defer timeout.Stop()
	for {
		errLock := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if errLock == nil {
			return func() { _ = f.Close() }, nil
		}
		if !errors.Is(errLock, unix.EWOULDBLOCK) && !errors.Is(errLock, unix.EINTR) {
			_ = f.Close()
			return nil, errors.New("cannot take the credential refresh lock")
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-timeout.C:
			_ = f.Close()
			return nil, errors.New("timed out waiting for another claude-master to finish refreshing this profile")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func credentialString(m map[string]any, key string) string {
	value, _ := m[key].(string)
	return strings.TrimSpace(value)
}

func credentialRevision(m map[string]any) time.Time {
	t, err := time.Parse(time.RFC3339, credentialString(m, "last_refresh"))
	if err != nil {
		return time.Time{}
	}
	return t
}

// diskAhead reports whether the saved credential holds a rotation the in-memory one has not seen.
// An in-memory credential that is newer (this process just refreshed and has not saved yet) is not
// overwritten.
func diskAhead(disk, mem map[string]any) bool {
	if credentialString(disk, "access_token") == "" || credentialString(disk, "refresh_token") == "" {
		return false
	}
	if credentialString(disk, "access_token") == credentialString(mem, "access_token") &&
		credentialString(disk, "refresh_token") == credentialString(mem, "refresh_token") {
		return false
	}
	diskRevision, memRevision := credentialRevision(disk), credentialRevision(mem)
	if diskRevision.IsZero() && memRevision.IsZero() {
		return false // no ordering information at all (an old file): this process's save stands
	}
	// A tie between differing token pairs is a conflict, not "disk is not newer": a save from a
	// process holding the previous pair must never overwrite a pair that was just rotated, because
	// the old refresh token is dead. Rotations stamp last_refresh at nanosecond precision, so a real
	// tie is vanishingly rare; when one happens the saved file wins.
	return !diskRevision.Before(memRevision)
}

// adoptDiskTokens returns a copy of auth carrying the saved credential's tokens. Everything else
// (identity, device pool, routing flags) stays this process's own.
func adoptDiskTokens(auth *coreauth.Auth, disk map[string]any) *coreauth.Auth {
	out := auth.Clone()
	if out.Metadata == nil {
		out.Metadata = make(map[string]any, len(credentialTokenKeys))
	}
	for _, key := range credentialTokenKeys {
		if value, ok := disk[key]; ok {
			out.Metadata[key] = value
		}
	}
	return out
}
