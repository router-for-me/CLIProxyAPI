package backup

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// Manager handles backup operations.
type Manager struct {
	configPath string
	authDir    string
	logsDir    string
	storage    Storage
}

// NewManager creates a new backup manager.
func NewManager(configPath, authDir, logsDir string, storage Storage) *Manager {
	return &Manager{
		configPath: configPath,
		authDir:    authDir,
		logsDir:    logsDir,
		storage:    storage,
	}
}

// SetStorage updates the storage backend.
func (m *Manager) SetStorage(storage Storage) {
	m.storage = storage
}

// Create creates a new backup and returns the backup data.
func (m *Manager) Create() ([]byte, string, error) {
	timestamp := time.Now().Format("20060102-150405")
	filename := fmt.Sprintf("backup-%s.zip", timestamp)

	buf := new(bytes.Buffer)
	zipWriter := zip.NewWriter(buf)

	// Backup config.yaml
	if err := m.addFileToZip(zipWriter, m.configPath, "config.yaml"); err != nil {
		log.WithError(err).Warn("failed to backup config.yaml")
	}

	// Backup auth directory (OAuth files)
	if m.authDir != "" {
		if err := m.addDirToZip(zipWriter, m.authDir, "auths"); err != nil {
			log.WithError(err).Warn("failed to backup auth directory")
		}
	}

	// Backup logs (optional, only recent logs)
	if m.logsDir != "" {
		if err := m.addRecentLogsToZip(zipWriter, m.logsDir, "logs"); err != nil {
			log.WithError(err).Warn("failed to backup logs")
		}
	}

	if err := zipWriter.Close(); err != nil {
		return nil, "", fmt.Errorf("failed to close zip writer: %w", err)
	}

	log.Infof("backup created: %s (size: %d bytes)", filename, buf.Len())
	return buf.Bytes(), filename, nil
}

// Upload creates a backup and uploads it to the configured storage.
func (m *Manager) Upload() (BackupInfo, error) {
	data, filename, err := m.Create()
	if err != nil {
		return BackupInfo{}, fmt.Errorf("failed to create backup: %w", err)
	}

	if err := m.storage.Upload(filename, data); err != nil {
		return BackupInfo{}, fmt.Errorf("failed to upload backup: %w", err)
	}

	info := BackupInfo{
		Name:      filename,
		Timestamp: time.Now(),
		Size:      int64(len(data)),
		Location:  filename,
	}

	log.Infof("backup uploaded: %s", filename)
	return info, nil
}

// List returns all available backups.
func (m *Manager) List() ([]BackupInfo, error) {
	return m.storage.List()
}

// Download retrieves a backup by name.
func (m *Manager) Download(name string) ([]byte, error) {
	return m.storage.Download(name)
}

// Delete removes a backup by name.
func (m *Manager) Delete(name string) error {
	return m.storage.Delete(name)
}

// CleanupOldBackups removes old backups exceeding the max count.
func (m *Manager) CleanupOldBackups(maxBackups int) error {
	if maxBackups <= 0 {
		return nil // No limit
	}

	backups, err := m.storage.List()
	if err != nil {
		return fmt.Errorf("failed to list backups: %w", err)
	}

	if len(backups) <= maxBackups {
		return nil // Within limit
	}

	// Sort by timestamp (oldest first)
	// Note: backups are typically already sorted by name due to timestamp format
	toDelete := len(backups) - maxBackups

	for i := 0; i < toDelete; i++ {
		if err := m.storage.Delete(backups[i].Name); err != nil {
			log.WithError(err).Warnf("failed to delete old backup: %s", backups[i].Name)
		} else {
			log.Infof("deleted old backup: %s", backups[i].Name)
		}
	}

	return nil
}

// addFileToZip adds a single file to the zip archive.
func (m *Manager) addFileToZip(zipWriter *zip.Writer, filePath, zipPath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Skip missing files
		}
		return fmt.Errorf("failed to open file %s: %w", filePath, err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat file %s: %w", filePath, err)
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return fmt.Errorf("failed to create zip header: %w", err)
	}
	header.Name = zipPath
	header.Method = zip.Deflate

	writer, err := zipWriter.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("failed to create zip entry: %w", err)
	}

	if _, err := io.Copy(writer, file); err != nil {
		return fmt.Errorf("failed to write file to zip: %w", err)
	}

	return nil
}

// addDirToZip adds a directory to the zip archive.
func (m *Manager) addDirToZip(zipWriter *zip.Writer, dirPath, zipBasePath string) error {
	return filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		// Skip temporary OAuth files
		if strings.HasPrefix(info.Name(), ".oauth-") {
			return nil
		}

		relPath, err := filepath.Rel(dirPath, path)
		if err != nil {
			return err
		}

		zipPath := filepath.Join(zipBasePath, relPath)
		// Normalize path separators for zip (always use forward slash)
		zipPath = filepath.ToSlash(zipPath)

		return m.addFileToZip(zipWriter, path, zipPath)
	})
}

// addRecentLogsToZip adds recent log files to the zip archive (last 10 files).
func (m *Manager) addRecentLogsToZip(zipWriter *zip.Writer, logsDir, zipBasePath string) error {
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Skip if logs directory doesn't exist
		}
		return fmt.Errorf("failed to read logs directory: %w", err)
	}

	// Limit to last 10 log files
	start := 0
	if len(entries) > 10 {
		start = len(entries) - 10
	}

	for i := start; i < len(entries); i++ {
		entry := entries[i]
		if entry.IsDir() {
			continue
		}

		logPath := filepath.Join(logsDir, entry.Name())
		zipPath := filepath.Join(zipBasePath, entry.Name())
		zipPath = filepath.ToSlash(zipPath)

		if err := m.addFileToZip(zipWriter, logPath, zipPath); err != nil {
			log.WithError(err).Warnf("failed to add log file %s", entry.Name())
		}
	}

	return nil
}

// Restore restores configuration from a backup zip file.
func (m *Manager) Restore(data []byte) error {
	// Create a reader from the backup data
	reader := bytes.NewReader(data)
	zipReader, err := zip.NewReader(reader, int64(len(data)))
	if err != nil {
		return fmt.Errorf("failed to open zip file: %w", err)
	}

	// Extract files from zip
	for _, file := range zipReader.File {
		if err := m.extractFile(file); err != nil {
			log.WithError(err).Warnf("failed to extract file: %s", file.Name)
			// Continue with other files even if one fails
		}
	}

	log.Info("backup restored successfully")
	return nil
}

// extractFile extracts a single file from the zip archive.
func (m *Manager) extractFile(file *zip.File) error {
	rc, err := file.Open()
	if err != nil {
		return fmt.Errorf("failed to open file in zip: %w", err)
	}
	defer rc.Close()

	// Determine target path
	var targetPath string
	if strings.HasPrefix(file.Name, "auths/") {
		// Extract to auth directory
		relativePath := strings.TrimPrefix(file.Name, "auths/")
		targetPath = filepath.Join(m.authDir, relativePath)
	} else if file.Name == "config.yaml" {
		// Extract to config path
		targetPath = m.configPath
	} else if strings.HasPrefix(file.Name, "logs/") {
		// Skip logs during restore (we don't want to overwrite current logs)
		log.Debugf("skipping log file during restore: %s", file.Name)
		return nil
	} else {
		// Unknown file, skip
		log.Warnf("skipping unknown file during restore: %s", file.Name)
		return nil
	}

	// Create directory if needed
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Create target file
	outFile, err := os.Create(targetPath)
	if err != nil {
		return fmt.Errorf("failed to create target file: %w", err)
	}
	defer outFile.Close()

	// Copy content
	if _, err := io.Copy(outFile, rc); err != nil {
		return fmt.Errorf("failed to write file content: %w", err)
	}

	log.Infof("restored file: %s", targetPath)
	return nil
}
