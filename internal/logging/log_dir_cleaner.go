package logging

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

const logDirCleanerInterval = time.Minute

var logDirCleanerCancel context.CancelFunc

func configureLogDirCleanerLocked(logDir string, maxTotalSizeMB int, retentionDays int, protectedPath string) {
	stopLogDirCleanerLocked()

	if maxTotalSizeMB <= 0 && retentionDays <= 0 {
		return
	}

	dir := strings.TrimSpace(logDir)
	if dir == "" {
		return
	}

	maxBytes := int64(0)
	if maxTotalSizeMB > 0 {
		maxBytes = int64(maxTotalSizeMB) * 1024 * 1024
	}

	ctx, cancel := context.WithCancel(context.Background())
	logDirCleanerCancel = cancel
	go runLogDirCleaner(ctx, filepath.Clean(dir), maxBytes, retentionDays, strings.TrimSpace(protectedPath))
}

func stopLogDirCleanerLocked() {
	if logDirCleanerCancel == nil {
		return
	}
	logDirCleanerCancel()
	logDirCleanerCancel = nil
}

func runLogDirCleaner(ctx context.Context, logDir string, maxBytes int64, retentionDays int, protectedPath string) {
	ticker := time.NewTicker(logDirCleanerInterval)
	defer ticker.Stop()

	cleanOnce := func() {
		if maxBytes > 0 {
			deleted, errClean := enforceLogDirSizeLimit(logDir, maxBytes, protectedPath)
			if errClean != nil {
				log.WithError(errClean).Warn("logging: failed to enforce log directory size limit")
			} else if deleted > 0 {
				log.Debugf("logging: removed %d old log file(s) to enforce log directory size limit", deleted)
			}
		}
		if retentionDays > 0 {
			deleted, errClean := enforceLogDirRetention(logDir, retentionDays, protectedPath)
			if errClean != nil {
				log.WithError(errClean).Warn("logging: failed to enforce log retention policy")
			} else if deleted > 0 {
				log.Debugf("logging: removed %d expired log file(s) older than %d day(s)", deleted, retentionDays)
			}
		}
	}

	cleanOnce()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanOnce()
		}
	}
}

func enforceLogDirSizeLimit(logDir string, maxBytes int64, protectedPath string) (int, error) {
	if maxBytes <= 0 {
		return 0, nil
	}

	dir := strings.TrimSpace(logDir)
	if dir == "" {
		return 0, nil
	}
	dir = filepath.Clean(dir)

	entries, errRead := os.ReadDir(dir)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			return 0, nil
		}
		return 0, errRead
	}

	protected := strings.TrimSpace(protectedPath)
	if protected != "" {
		protected = filepath.Clean(protected)
	}

	type logFile struct {
		path    string
		size    int64
		modTime time.Time
	}

	var (
		files []logFile
		total int64
	)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isLogFileName(name) {
			continue
		}
		info, errInfo := entry.Info()
		if errInfo != nil {
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		path := filepath.Join(dir, name)
		files = append(files, logFile{
			path:    path,
			size:    info.Size(),
			modTime: info.ModTime(),
		})
		total += info.Size()
	}

	if total <= maxBytes {
		return 0, nil
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.Before(files[j].modTime)
	})

	deleted := 0
	for _, file := range files {
		if total <= maxBytes {
			break
		}
		if protected != "" && filepath.Clean(file.path) == protected {
			continue
		}
		if errRemove := os.Remove(file.path); errRemove != nil {
			log.WithError(errRemove).Warnf("logging: failed to remove old log file: %s", filepath.Base(file.path))
			continue
		}
		total -= file.size
		deleted++
	}

	return deleted, nil
}

// enforceLogDirRetention removes log files older than the specified number of days.
func enforceLogDirRetention(logDir string, retentionDays int, protectedPath string) (int, error) {
	if retentionDays <= 0 {
		return 0, nil
	}

	dir := strings.TrimSpace(logDir)
	if dir == "" {
		return 0, nil
	}
	dir = filepath.Clean(dir)

	entries, errRead := os.ReadDir(dir)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			return 0, nil
		}
		return 0, errRead
	}

	protected := strings.TrimSpace(protectedPath)
	if protected != "" {
		protected = filepath.Clean(protected)
	}

	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	deleted := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isLogFileName(name) {
			continue
		}
		info, errInfo := entry.Info()
		if errInfo != nil {
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		path := filepath.Join(dir, name)
		if protected != "" && filepath.Clean(path) == protected {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if errRemove := os.Remove(path); errRemove != nil {
				log.WithError(errRemove).Warnf("logging: failed to remove expired log file: %s", name)
				continue
			}
			deleted++
		}
	}

	return deleted, nil
}

// CleanupRequestLogs removes all request log files from the given directory.
// It skips the main application log file and the protected path.
func CleanupRequestLogs(logDir string, protectedPath string) (int, error) {
	dir := strings.TrimSpace(logDir)
	if dir == "" {
		return 0, nil
	}
	dir = filepath.Clean(dir)

	entries, errRead := os.ReadDir(dir)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			return 0, nil
		}
		return 0, errRead
	}

	protected := strings.TrimSpace(protectedPath)
	if protected != "" {
		protected = filepath.Clean(protected)
	}

	deleted := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Skip the main application log and its rotations
		if name == "main.log" {
			continue
		}
		if !isLogFileName(name) {
			continue
		}
		path := filepath.Join(dir, name)
		if protected != "" && filepath.Clean(path) == protected {
			continue
		}
		if errRemove := os.Remove(path); errRemove != nil {
			log.WithError(errRemove).Warnf("logging: failed to remove request log file: %s", name)
			continue
		}
		deleted++
	}

	return deleted, nil
}

func isLogFileName(name string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	return strings.HasSuffix(lower, ".log") || strings.HasSuffix(lower, ".log.gz")
}
