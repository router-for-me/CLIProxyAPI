package logging

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// staleRequestLogTempAge is deliberately much longer than a normal inference
// request so a second process sharing the log directory cannot remove live
// spool files during startup.
const staleRequestLogTempAge = 24 * time.Hour

func isRequestLogTempArtifact(name string, isDir bool) bool {
	if isDir {
		return strings.HasPrefix(name, "request-log-parts-")
	}
	return (strings.HasPrefix(name, "request-body-") || strings.HasPrefix(name, "response-body-")) && strings.HasSuffix(name, ".tmp")
}

// cleanupStaleRequestLogTempArtifacts removes request-log spool artifacts left
// behind by an unclean shutdown. Recent artifacts are retained because another
// process may still be using the same log directory.
func (l *FileRequestLogger) cleanupStaleRequestLogTempArtifacts(now time.Time, maxAge time.Duration) error {
	if l == nil || strings.TrimSpace(l.logsDir) == "" || maxAge <= 0 {
		return nil
	}
	entries, errRead := os.ReadDir(l.logsDir)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			return nil
		}
		return errRead
	}
	if now.IsZero() {
		now = time.Now()
	}
	cutoff := now.Add(-maxAge)
	for _, entry := range entries {
		if !isRequestLogTempArtifact(entry.Name(), entry.IsDir()) {
			continue
		}
		path := filepath.Join(l.logsDir, entry.Name())
		lastModified, errModified := latestArtifactModTime(path)
		if errModified != nil {
			if !os.IsNotExist(errModified) {
				log.WithError(errModified).Warnf("failed to inspect request-log temp artifact: %s", entry.Name())
			}
			continue
		}
		if !lastModified.Before(cutoff) {
			continue
		}
		var errRemove error
		if entry.IsDir() {
			errRemove = os.RemoveAll(path)
		} else {
			errRemove = os.Remove(path)
		}
		if errRemove != nil && !os.IsNotExist(errRemove) {
			log.WithError(errRemove).Warnf("failed to remove stale request-log temp artifact: %s", entry.Name())
		}
	}
	return nil
}

// latestArtifactModTime returns the newest modification time in an artifact.
// Checking directory contents protects a long-running spool whose directory
// itself is old but whose current part file is still being written.
func latestArtifactModTime(path string) (time.Time, error) {
	info, errStat := os.Stat(path)
	if errStat != nil {
		return time.Time{}, errStat
	}
	latest := info.ModTime()
	if !info.IsDir() {
		return latest, nil
	}
	errWalk := filepath.WalkDir(path, func(childPath string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		childInfo, errInfo := entry.Info()
		if errInfo != nil {
			return errInfo
		}
		if childInfo.ModTime().After(latest) {
			latest = childInfo.ModTime()
		}
		return nil
	})
	return latest, errWalk
}
