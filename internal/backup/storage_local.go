package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	log "github.com/sirupsen/logrus"
)

// LocalStorage implements Storage interface for local filesystem.
type LocalStorage struct {
	baseDir string
}

// NewLocalStorage creates a new local storage backend.
func NewLocalStorage(baseDir string) (*LocalStorage, error) {
	// Expand home directory
	if strings.HasPrefix(baseDir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		baseDir = filepath.Join(home, baseDir[2:])
	}

	// Create directory if it doesn't exist
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create backup directory: %w", err)
	}

	return &LocalStorage{baseDir: baseDir}, nil
}

// Upload saves a backup file to local storage.
func (s *LocalStorage) Upload(filename string, data []byte) error {
	path := filepath.Join(s.baseDir, filename)

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write backup file: %w", err)
	}

	log.Infof("backup saved to local storage: %s", path)
	return nil
}

// List returns all available backups in local storage.
func (s *LocalStorage) List() ([]BackupInfo, error) {
	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []BackupInfo{}, nil
		}
		return nil, fmt.Errorf("failed to read backup directory: %w", err)
	}

	var backups []BackupInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".zip") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			log.WithError(err).Warnf("failed to get info for backup: %s", entry.Name())
			continue
		}

		backups = append(backups, BackupInfo{
			Name:      entry.Name(),
			Timestamp: info.ModTime(),
			Size:      info.Size(),
			Storage:   StorageTypeLocal,
			Location:  filepath.Join(s.baseDir, entry.Name()),
		})
	}

	// Sort by name (which includes timestamp)
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Name < backups[j].Name
	})

	return backups, nil
}

// Download retrieves a backup file from local storage.
func (s *LocalStorage) Download(name string) ([]byte, error) {
	// Validate filename to prevent path traversal
	if strings.Contains(name, "..") || strings.Contains(name, string(filepath.Separator)) {
		return nil, fmt.Errorf("invalid backup name: %s", name)
	}

	path := filepath.Join(s.baseDir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("backup not found: %s", name)
		}
		return nil, fmt.Errorf("failed to read backup file: %w", err)
	}

	return data, nil
}

// Delete removes a backup file from local storage.
func (s *LocalStorage) Delete(name string) error {
	// Validate filename to prevent path traversal
	if strings.Contains(name, "..") || strings.Contains(name, string(filepath.Separator)) {
		return fmt.Errorf("invalid backup name: %s", name)
	}

	path := filepath.Join(s.baseDir, name)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("backup not found: %s", name)
		}
		return fmt.Errorf("failed to delete backup file: %w", err)
	}

	log.Infof("backup deleted from local storage: %s", name)
	return nil
}

// TestConnection tests if the local storage directory is accessible.
func (s *LocalStorage) TestConnection() error {
	// Try to create the directory if it doesn't exist
	if err := os.MkdirAll(s.baseDir, 0755); err != nil {
		return fmt.Errorf("failed to access backup directory: %w", err)
	}

	// Try to write a test file
	testFile := filepath.Join(s.baseDir, ".test")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		return fmt.Errorf("failed to write test file: %w", err)
	}

	// Clean up test file
	if err := os.Remove(testFile); err != nil {
		log.WithError(err).Warn("failed to remove test file")
	}

	return nil
}
