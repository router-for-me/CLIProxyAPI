package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteConfigAtomicExact atomically persists the supplied bytes without
// normalization. The caller serializes writes with the configuration owner.
// An error syncing the directory after rename leaves durability uncertain;
// callers must reconcile with the file before retrying.
func WriteConfigAtomicExact(path string, data []byte) (returnErr error) {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("config path is empty")
	}
	dir := filepath.Dir(path)
	mode := os.FileMode(0o600)
	if info, errStat := os.Stat(path); errStat == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(errStat) {
		return errStat
	}
	file, errCreate := os.CreateTemp(dir, ".config-write-*.yaml")
	if errCreate != nil {
		return errCreate
	}
	tempPath := file.Name()
	committed := false
	defer func() {
		if file != nil {
			returnErr = errors.Join(returnErr, file.Close())
		}
		if !committed {
			if errRemove := os.Remove(tempPath); errRemove != nil && !os.IsNotExist(errRemove) {
				returnErr = errors.Join(returnErr, fmt.Errorf("remove uncommitted config staging file: %w", errRemove))
			}
		}
	}()
	if errChmod := file.Chmod(mode); errChmod != nil {
		return fmt.Errorf("set config staging permissions: %w", errChmod)
	}
	if _, errWrite := file.Write(data); errWrite != nil {
		return fmt.Errorf("write config staging file: %w", errWrite)
	}
	if errSync := file.Sync(); errSync != nil {
		return fmt.Errorf("sync config staging file: %w", errSync)
	}
	if errClose := file.Close(); errClose != nil {
		file = nil
		return fmt.Errorf("close config staging file: %w", errClose)
	}
	file = nil

	directory, errOpen := os.Open(dir)
	if errOpen != nil {
		return fmt.Errorf("open config directory: %w", errOpen)
	}
	if errRename := os.Rename(tempPath, path); errRename != nil {
		return errors.Join(fmt.Errorf("activate staged config: %w", errRename), directory.Close())
	}
	committed = true
	errSync := directory.Sync()
	errClose := directory.Close()
	if errSync != nil {
		errSync = fmt.Errorf("sync config directory: %w", errSync)
	}
	if errClose != nil {
		errClose = fmt.Errorf("close config directory: %w", errClose)
	}
	return errors.Join(errSync, errClose)
}
