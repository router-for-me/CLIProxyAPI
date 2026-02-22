package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrConcurrentGitWrite = fmt.Errorf("concurrent git write in progress")

func isGitErr(err error, fragment string) bool {
	if err == nil {
		return false
	}
	if strings.TrimSpace(fragment) == "" {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), strings.ToLower(strings.TrimSpace(fragment)))
}

func isNonFastForwardUpdateError(err error) bool {
	if err == nil {
		return false
	}
	if isGitErr(err, "non-fast-forward") {
		return true
	}
	return false
}

func bootstrapPullDivergedError(err error) error {
	if !isNonFastForwardUpdateError(err) {
		return fmt.Errorf("bootstrap pull failed: %w", err)
	}
	return fmt.Errorf("%w: bootstrap pull diverged, please retry after sync: %w", ErrConcurrentGitWrite, err)
}

func snapshotLocalAuthFiles(authDir string) (map[string]int64, error) {
	authDir = strings.TrimSpace(authDir)
	if authDir == "" {
		return nil, fmt.Errorf("auth directory is required")
	}

	info := make(map[string]int64)
	err := filepath.Walk(authDir, func(path string, _ os.FileInfo, errWalk error) error {
		if errWalk != nil {
			return errWalk
		}
		if !strings.HasSuffix(strings.ToLower(filepath.Base(path)), ".json") {
			return nil
		}
		st, errStat := os.Stat(path)
		if errStat != nil {
			return errStat
		}
		if st.IsDir() {
			return nil
		}
		info[path] = st.ModTime().UnixNano()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return info, nil
}

func buildSafeAuthPrunePlan(authDir string, baseline map[string]int64, remote map[string]struct{}) ([]string, []string, error) {
	if strings.TrimSpace(authDir) == "" {
		return nil, nil, fmt.Errorf("auth directory is required")
	}
	if baseline == nil {
		baseline = make(map[string]int64)
	}
	if remote == nil {
		remote = make(map[string]struct{})
	}

	isRemote := func(path string) bool {
		base := filepath.Base(path)
		_, ok := remote[base]
		return ok
	}
	current := make(map[string]int64)
	if err := filepath.Walk(authDir, func(path string, info os.FileInfo, errWalk error) error {
		if errWalk != nil {
			return errWalk
		}
		if info == nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(info.Name()), ".json") {
			return nil
		}
		current[path] = info.ModTime().UnixNano()
		return nil
	}); err != nil {
		return nil, nil, err
	}

	stale := make([]string, 0)
	conflicts := make([]string, 0)

	for path, baselineTs := range baseline {
		if isRemote(path) {
			continue
		}
		if ts, ok := current[path]; !ok {
			stale = append(stale, path)
		} else if ts == baselineTs {
			stale = append(stale, path)
		} else {
			conflicts = append(conflicts, path)
		}
	}

	for path := range current {
		if isRemote(path) {
			continue
		}
		if _, ok := baseline[path]; !ok {
			conflicts = append(conflicts, path)
		}
	}

	return stale, conflicts, nil
}
