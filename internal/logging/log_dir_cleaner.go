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

type logDirCleanupOptions struct {
	maxBytes           int64
	requestLogMaxFiles int
	requestLogMaxDays  int
	protectedPath      string
}

func configureLogDirCleanerLocked(logDir string, maxTotalSizeMB int, requestLogMaxFiles int, requestLogMaxDays int, protectedPath string) {
	stopLogDirCleanerLocked()

	dir := strings.TrimSpace(logDir)
	if dir == "" {
		return
	}

	opts := logDirCleanupOptions{
		requestLogMaxFiles: requestLogMaxFiles,
		requestLogMaxDays:  requestLogMaxDays,
		protectedPath:      strings.TrimSpace(protectedPath),
	}
	if maxTotalSizeMB > 0 {
		opts.maxBytes = int64(maxTotalSizeMB) * 1024 * 1024
	}
	if opts.maxBytes <= 0 && opts.requestLogMaxFiles <= 0 && opts.requestLogMaxDays <= 0 {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	logDirCleanerCancel = cancel
	go runLogDirCleaner(ctx, filepath.Clean(dir), opts)
}

func stopLogDirCleanerLocked() {
	if logDirCleanerCancel == nil {
		return
	}
	logDirCleanerCancel()
	logDirCleanerCancel = nil
}

func runLogDirCleaner(ctx context.Context, logDir string, opts logDirCleanupOptions) {
	ticker := time.NewTicker(logDirCleanerInterval)
	defer ticker.Stop()

	cleanOnce := func() {
		requestDeleted, errRequestClean := enforceRequestLogRetention(logDir, opts.requestLogMaxFiles, opts.requestLogMaxDays)
		if errRequestClean != nil {
			log.WithError(errRequestClean).Warn("logging: failed to enforce request log retention")
		} else if requestDeleted > 0 {
			log.Debugf("logging: removed %d request log file(s) due to retention policy", requestDeleted)
		}

		sizeDeleted, errSizeClean := enforceLogDirSizeLimit(logDir, opts.maxBytes, opts.protectedPath)
		if errSizeClean != nil {
			log.WithError(errSizeClean).Warn("logging: failed to enforce log directory size limit")
			return
		}
		if sizeDeleted > 0 {
			log.Debugf("logging: removed %d old log file(s) to enforce log directory size limit", sizeDeleted)
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

func enforceRequestLogRetention(logDir string, maxFiles int, maxDays int) (int, error) {
	if maxFiles <= 0 && maxDays <= 0 {
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

	type requestLogFile struct {
		path    string
		modTime time.Time
	}

	var files []requestLogFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isRequestLogFileName(name) {
			continue
		}
		info, errInfo := entry.Info()
		if errInfo != nil {
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		files = append(files, requestLogFile{
			path:    filepath.Join(dir, name),
			modTime: info.ModTime(),
		})
	}

	deleted := 0
	if maxDays > 0 {
		cutoff := time.Now().Add(-time.Duration(maxDays) * 24 * time.Hour)
		filtered := files[:0]
		for _, file := range files {
			if file.modTime.Before(cutoff) {
				if errRemove := os.Remove(file.path); errRemove != nil {
					log.WithError(errRemove).Warnf("logging: failed to remove expired request log file: %s", filepath.Base(file.path))
					filtered = append(filtered, file)
					continue
				}
				deleted++
				continue
			}
			filtered = append(filtered, file)
		}
		files = filtered
	}

	if maxFiles > 0 && len(files) > maxFiles {
		sort.Slice(files, func(i, j int) bool {
			return files[i].modTime.After(files[j].modTime)
		})
		for _, file := range files[maxFiles:] {
			if errRemove := os.Remove(file.path); errRemove != nil {
				log.WithError(errRemove).Warnf("logging: failed to remove excess request log file: %s", filepath.Base(file.path))
				continue
			}
			deleted++
		}
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

func isRequestLogFileName(name string) bool {
	trimmed := strings.TrimSpace(name)
	if !isLogFileName(trimmed) {
		return false
	}
	return !isMainApplicationLogFileName(trimmed)
}

func isMainApplicationLogFileName(name string) bool {
	trimmed := strings.TrimSpace(name)
	lower := strings.ToLower(trimmed)
	if lower == "main.log" || strings.HasPrefix(lower, "main.log.") {
		return true
	}
	if !strings.HasPrefix(trimmed, "main-") {
		return false
	}

	clean := strings.TrimPrefix(trimmed, "main-")
	if strings.HasSuffix(clean, ".gz") {
		clean = strings.TrimSuffix(clean, ".gz")
	}
	if !strings.HasSuffix(clean, ".log") {
		return false
	}
	clean = strings.TrimSuffix(clean, ".log")
	if idx := strings.IndexByte(clean, '.'); idx != -1 {
		clean = clean[:idx]
	}
	if clean == "" {
		return false
	}
	_, err := time.ParseInLocation("2006-01-02T15-04-05", clean, time.Local)
	return err == nil
}
