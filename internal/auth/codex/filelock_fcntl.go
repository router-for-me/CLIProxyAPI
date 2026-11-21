//go:build aix || solaris

package codex

import (
	"fmt"

	"github.com/gofrs/flock"
	log "github.com/sirupsen/logrus"
)

func acquireInterProcessLockFile(authFilePath string) (func(), error) {
	lockPath := canonicalLockPath(authFilePath) + ".lock"
	lock := flock.New(lockPath)
	if errLock := lock.Lock(); errLock != nil {
		return nil, fmt.Errorf("failed to lock credential file %s: %w", lockPath, errLock)
	}
	return func() {
		if errClose := lock.Close(); errClose != nil {
			log.Errorf("codex token storage: release credential file lock: %v", errClose)
		}
	}, nil
}
