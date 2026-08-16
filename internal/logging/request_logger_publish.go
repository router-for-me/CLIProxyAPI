package logging

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func publishRequestLog(filePath string, writeLog func(*os.File) error) (err error) {
	tempFile, errCreate := os.CreateTemp(filepath.Dir(filePath), ".request-log-*.tmp")
	if errCreate != nil {
		return fmt.Errorf("create request log temp file: %w", errCreate)
	}
	tempPath := tempFile.Name()
	closed := false
	defer func() {
		if !closed {
			if errClose := tempFile.Close(); errClose != nil && err == nil {
				err = fmt.Errorf("close request log temp file: %w", errClose)
			}
		}
		if errRemove := os.Remove(tempPath); errRemove != nil && !os.IsNotExist(errRemove) {
			if err == nil {
				err = fmt.Errorf("remove request log temp file: %w", errRemove)
			} else {
				err = fmt.Errorf("%w; remove request log temp file: %v", err, errRemove)
			}
		}
	}()

	if errChmod := tempFile.Chmod(0o644); errChmod != nil {
		return fmt.Errorf("set request log temp file permissions: %w", errChmod)
	}
	if writeLog == nil {
		return fmt.Errorf("request log writer is nil")
	}
	if errWrite := writeLog(tempFile); errWrite != nil {
		return fmt.Errorf("write request log temp file: %w", errWrite)
	}
	if errSync := tempFile.Sync(); errSync != nil {
		return fmt.Errorf("sync request log temp file: %w", errSync)
	}
	if errClose := tempFile.Close(); errClose != nil {
		closed = true
		return fmt.Errorf("close request log temp file: %w", errClose)
	}
	closed = true
	publishedPath, errPublish := linkRequestLogWithoutOverwrite(tempPath, filePath)
	if errPublish != nil {
		return errPublish
	}
	if errRemove := os.Remove(tempPath); errRemove != nil {
		return fmt.Errorf("remove linked request log temp file: %w", errRemove)
	}
	if errSyncDir := syncRequestLogDirectory(publishedPath); errSyncDir != nil {
		return errSyncDir
	}
	return nil
}

func linkRequestLogWithoutOverwrite(tempPath, filePath string) (string, error) {
	extension := filepath.Ext(filePath)
	base := strings.TrimSuffix(filePath, extension)
	for attempt := uint64(0); ; attempt++ {
		candidate := filePath
		if attempt > 0 {
			candidate = fmt.Sprintf("%s-duplicate-%d%s", base, requestLogID.Add(1), extension)
		}
		if errLink := os.Link(tempPath, candidate); errLink == nil {
			return candidate, nil
		} else if !os.IsExist(errLink) {
			return "", fmt.Errorf("publish request log without overwrite: %w", errLink)
		}
	}
}

func syncRequestLogDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, errOpen := os.Open(filepath.Dir(path))
	if errOpen != nil {
		return fmt.Errorf("open request log directory for sync: %w", errOpen)
	}
	errSync := directory.Sync()
	errClose := directory.Close()
	if errCombined := errors.Join(errSync, errClose); errCombined != nil {
		return fmt.Errorf("sync request log directory: %w", errCombined)
	}
	return nil
}
