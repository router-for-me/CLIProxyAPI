package securefile

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
)

// MigrateAuthJSONDir rewrites auth JSON files in authDir to match settings.
// It returns the list of files that were updated.
func MigrateAuthJSONDir(authDir string, settings AuthEncryptionSettings) ([]string, error) {
	root := strings.TrimSpace(authDir)
	if root == "" {
		return nil, nil
	}

	var (
		changed []string
		errs    []error
	)

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			errs = append(errs, err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			return nil
		}
		raw, err := ReadFileRawLocked(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			errs = append(errs, fmt.Errorf("read auth file %s: %w", path, err))
			return nil
		}
		if len(raw) == 0 {
			return nil
		}
		plaintext, wasEncrypted, err := DecodeAuthJSON(raw, settings)
		if err != nil {
			errs = append(errs, fmt.Errorf("decode auth file %s: %w", path, err))
			return nil
		}

		if settings.Enabled && wasEncrypted {
			return nil
		}
		if !settings.Enabled && !wasEncrypted {
			return nil
		}

		lockPath := path + ".lock"
		if err := WithLock(lockPath, 10*time.Second, func() error {
			return writeAuthJSONFileUnlocked(path, plaintext, settings)
		}); err != nil {
			errs = append(errs, fmt.Errorf("write auth file %s: %w", path, err))
			return nil
		}
		changed = append(changed, path)
		return nil
	})
	if walkErr != nil {
		errs = append(errs, walkErr)
	}
	if len(errs) > 0 {
		return changed, errors.Join(errs...)
	}
	return changed, nil
}
