// Package backup provides secure backup and restore functionality for CLI Proxy API.
// It handles zip archive creation/extraction with protection against Zip Slip attacks
// and other security vulnerabilities.
package backup

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// BackupContent specifies what to include in a backup
type BackupContent struct {
	Env    bool `json:"env"`
	Config bool `json:"config"`
	Auths  bool `json:"auths"`
}

// BackupMetadata contains information about a backup archive
type BackupMetadata struct {
	Name    string        `json:"name"`
	Date    string        `json:"date"`
	Size    int64         `json:"size"`
	Content BackupContent `json:"content"`
}

// BackupOptions configures backup creation
type BackupOptions struct {
	Name       string
	BackupPath string
	Content    BackupContent
	WorkDir    string
	AuthDir    string
}

// RestoreOptions configures backup restoration
type RestoreOptions struct {
	BackupPath string
	BackupName string
	AuthsMode  string // "overwrite" or "incremental"
	WorkDir    string
	AuthDir    string
}

// SafeJoinPath safely joins a base directory with a relative path from a zip entry.
// It normalizes the path and validates that the result stays within the base directory.
// Returns an error if the path would escape the base directory (Zip Slip attack).
func SafeJoinPath(baseDir, zipEntryName string) (string, error) {
	// Normalize zip entry name (zip uses forward slashes)
	cleanZipPath := path.Clean(zipEntryName)

	// Reject absolute paths and paths starting with ..
	if path.IsAbs(cleanZipPath) || strings.HasPrefix(cleanZipPath, "..") {
		return "", fmt.Errorf("invalid zip entry path: %s", zipEntryName)
	}

	// Convert to OS-specific path and join with base
	relPath := filepath.FromSlash(cleanZipPath)
	targetPath := filepath.Join(baseDir, relPath)

	// Clean the target path
	targetPath = filepath.Clean(targetPath)

	// Verify the target is still under base directory using filepath.Rel
	rel, err := filepath.Rel(baseDir, targetPath)
	if err != nil {
		return "", fmt.Errorf("failed to compute relative path: %w", err)
	}

	// If rel starts with "..", the target is outside base
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("zip slip vulnerability detected: path %q escapes base directory", zipEntryName)
	}

	return targetPath, nil
}

// AddFileToZip adds a single file to a zip archive
func AddFileToZip(zipWriter *zip.Writer, filePath, zipPath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file %s: %w", filePath, err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat file %s: %w", filePath, err)
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return fmt.Errorf("failed to create zip header for %s: %w", filePath, err)
	}
	header.Name = zipPath
	header.Method = zip.Deflate

	writer, err := zipWriter.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("failed to create zip entry for %s: %w", zipPath, err)
	}

	if _, err = io.Copy(writer, file); err != nil {
		return fmt.Errorf("failed to write file %s to zip: %w", filePath, err)
	}

	return nil
}

// AddDirToZip recursively adds a directory to a zip archive
func AddDirToZip(zipWriter *zip.Writer, dirPath, zipBasePath string) error {
	return filepath.Walk(dirPath, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip symlinks for security
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}

		relPath, err := filepath.Rel(dirPath, filePath)
		if err != nil {
			return fmt.Errorf("failed to get relative path: %w", err)
		}

		// Use forward slashes in zip paths
		zipPath := path.Join(zipBasePath, filepath.ToSlash(relPath))

		if info.IsDir() {
			if relPath != "." {
				_, err := zipWriter.Create(zipPath + "/")
				return err
			}
			return nil
		}

		return AddFileToZip(zipWriter, filePath, zipPath)
	})
}

// WriteMetadata writes backup metadata to the zip archive
func WriteMetadata(zipWriter *zip.Writer, metadata BackupMetadata) error {
	metadataBytes, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal backup metadata: %w", err)
	}

	metaWriter, err := zipWriter.Create("backup_metadata.json")
	if err != nil {
		return fmt.Errorf("failed to create metadata file in zip: %w", err)
	}

	if _, err := metaWriter.Write(metadataBytes); err != nil {
		return fmt.Errorf("failed to write metadata to zip: %w", err)
	}

	return nil
}

// ExtractFile safely extracts a single file from a zip entry to the target path.
// It applies safe permissions and validates the target path.
func ExtractFile(f *zip.File, targetPath string, perm os.FileMode) error {
	// Ensure parent directory exists
	parentDir := filepath.Dir(targetPath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return fmt.Errorf("failed to create parent directory: %w", err)
	}

	// Check if parent is a symlink (security)
	if isSymlink, err := isPathSymlink(parentDir); err != nil {
		return fmt.Errorf("failed to check parent directory: %w", err)
	} else if isSymlink {
		return fmt.Errorf("parent directory is a symlink, refusing to write for security")
	}

	// Open zip file entry
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("failed to open zip entry: %w", err)
	}
	defer rc.Close()

	// Create target file with safe permissions
	outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer outFile.Close()

	// Limit read size to prevent zip bombs (100MB per file)
	const maxFileSize = 100 * 1024 * 1024
	limitedReader := io.LimitReader(rc, maxFileSize+1)

	n, err := io.Copy(outFile, limitedReader)
	if err != nil {
		return fmt.Errorf("failed to extract file: %w", err)
	}
	if n > maxFileSize {
		os.Remove(targetPath)
		return fmt.Errorf("file exceeds maximum allowed size of 100MB")
	}

	return nil
}

// isPathSymlink checks if any component of the path is a symlink
func isPathSymlink(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return info.Mode()&os.ModeSymlink != 0, nil
}

// CreateBackup creates a backup archive with the specified options
func CreateBackup(opts BackupOptions) (string, error) {
	// Validate backup directory
	backupDir := opts.BackupPath
	if backupDir == "" {
		backupDir = filepath.Join(opts.WorkDir, "backup")
	}

	// Ensure backup directory exists
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create backup directory: %w", err)
	}

	// Generate backup filename
	backupName := opts.Name
	if backupName == "" {
		backupName = fmt.Sprintf("cliProxyApi_backup_%s.zip", time.Now().Format("20060102_150405"))
	}
	if !strings.HasSuffix(backupName, ".zip") {
		backupName += ".zip"
	}

	backupPath := filepath.Join(backupDir, backupName)

	// Create zip file with restrictive permissions
	zipFile, err := os.OpenFile(backupPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", fmt.Errorf("failed to create backup file: %w", err)
	}
	defer zipFile.Close()

	zipWriter := zip.NewWriter(zipFile)
	defer zipWriter.Close()

	// Backup .env if requested
	if opts.Content.Env {
		envPath := filepath.Join(opts.WorkDir, ".env")
		if _, err := os.Stat(envPath); err == nil {
			if err := AddFileToZip(zipWriter, envPath, ".env"); err != nil {
				return "", fmt.Errorf("failed to backup .env: %w", err)
			}
		}
	}

	// Backup config.yaml if requested
	if opts.Content.Config {
		configPath := filepath.Join(opts.WorkDir, "config.yaml")
		if _, err := os.Stat(configPath); err == nil {
			if err := AddFileToZip(zipWriter, configPath, "config.yaml"); err != nil {
				return "", fmt.Errorf("failed to backup config.yaml: %w", err)
			}
		}
	}

	// Backup auths directory if requested
	if opts.Content.Auths {
		if _, err := os.Stat(opts.AuthDir); err == nil {
			if err := AddDirToZip(zipWriter, opts.AuthDir, "auths"); err != nil {
				return "", fmt.Errorf("failed to backup auths directory: %w", err)
			}
		}
	}

	// Write metadata
	metadata := BackupMetadata{
		Name:    backupName,
		Date:    time.Now().Format(time.RFC3339),
		Content: opts.Content,
	}
	if err := WriteMetadata(zipWriter, metadata); err != nil {
		return "", err
	}

	return backupPath, nil
}

// RestoreBackup restores a backup archive with the specified options
func RestoreBackup(opts RestoreOptions) error {
	backupPath := filepath.Join(opts.BackupPath, opts.BackupName)

	// Open the zip file
	zipReader, err := zip.OpenReader(backupPath)
	if err != nil {
		return fmt.Errorf("failed to open backup file: %w", err)
	}
	defer zipReader.Close()

	// If overwrite mode for auths, remove existing directory first
	if opts.AuthsMode == "overwrite" {
		// Safety check: don't remove if authDir looks suspicious
		cleanAuthDir := filepath.Clean(opts.AuthDir)
		if cleanAuthDir == "" || cleanAuthDir == "/" || cleanAuthDir == "." ||
			cleanAuthDir == filepath.VolumeName(cleanAuthDir)+"\\" {
			return fmt.Errorf("refusing to remove potentially dangerous auth directory: %s", opts.AuthDir)
		}

		if err := os.RemoveAll(opts.AuthDir); err != nil {
			return fmt.Errorf("failed to remove existing auth directory: %w", err)
		}
		if err := os.MkdirAll(opts.AuthDir, 0755); err != nil {
			return fmt.Errorf("failed to recreate auth directory: %w", err)
		}
	}

	// Extract files
	for _, f := range zipReader.File {
		if f.FileInfo().IsDir() {
			continue
		}

		var destPath string
		var perm os.FileMode = 0644

		switch {
		case f.Name == ".env":
			destPath = filepath.Join(opts.WorkDir, ".env")
			perm = 0600 // Sensitive file
		case f.Name == "config.yaml":
			destPath = filepath.Join(opts.WorkDir, "config.yaml")
			perm = 0600 // Sensitive file
		case strings.HasPrefix(f.Name, "auths/"):
			relativePath := strings.TrimPrefix(f.Name, "auths/")
			if relativePath == "" {
				continue
			}
			// Use SafeJoinPath to prevent Zip Slip
			var err error
			destPath, err = SafeJoinPath(opts.AuthDir, relativePath)
			if err != nil {
				return fmt.Errorf("invalid auth file path in backup: %w", err)
			}
			perm = 0600 // Auth files are sensitive
		case f.Name == "backup_metadata.json":
			continue // Skip metadata file
		default:
			continue // Skip unknown files
		}

		if err := ExtractFile(f, destPath, perm); err != nil {
			return fmt.Errorf("failed to extract %s: %w", f.Name, err)
		}
	}

	return nil
}

// ListBackups returns a list of backup metadata from the backup directory
func ListBackups(backupDir string) ([]BackupMetadata, error) {
	var backups []BackupMetadata

	entries, err := os.ReadDir(backupDir)
	if err != nil {
		if os.IsNotExist(err) {
			return backups, nil
		}
		return nil, fmt.Errorf("failed to read backup directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".zip") {
			continue
		}

		zipPath := filepath.Join(backupDir, entry.Name())
		metadata, err := ReadBackupMetadata(zipPath)
		if err != nil {
			// If we can't read metadata, create basic info from file
			info, errInfo := entry.Info()
			if errInfo != nil {
				// Cannot get file info, skip this backup entry
				continue
			}
			metadata = BackupMetadata{
				Name: entry.Name(),
				Date: info.ModTime().Format(time.RFC3339),
				Size: info.Size(),
			}
		} else {
			// Update size from actual file
			if info, err := entry.Info(); err == nil {
				metadata.Size = info.Size()
			}
		}

		backups = append(backups, metadata)
	}

	return backups, nil
}

// ReadBackupMetadata reads metadata from a backup archive
func ReadBackupMetadata(zipPath string) (BackupMetadata, error) {
	var metadata BackupMetadata

	zipReader, err := zip.OpenReader(zipPath)
	if err != nil {
		return metadata, err
	}
	defer zipReader.Close()

	for _, f := range zipReader.File {
		if f.Name == "backup_metadata.json" {
			rc, err := f.Open()
			if err != nil {
				return metadata, err
			}
			defer rc.Close()

			if err := json.NewDecoder(rc).Decode(&metadata); err != nil {
				return metadata, err
			}
			return metadata, nil
		}
	}

	return metadata, fmt.Errorf("metadata not found in backup")
}

// DeleteBackup removes a backup file
func DeleteBackup(backupDir, backupName string) error {
	// Validate backup name to prevent path traversal
	if strings.Contains(backupName, "/") || strings.Contains(backupName, "\\") ||
		strings.Contains(backupName, "..") {
		return fmt.Errorf("invalid backup name")
	}

	backupPath := filepath.Join(backupDir, backupName)

	// Verify it's actually under the backup directory
	cleanPath := filepath.Clean(backupPath)
	cleanDir := filepath.Clean(backupDir)
	rel, err := filepath.Rel(cleanDir, cleanPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("invalid backup path")
	}

	return os.Remove(backupPath)
}

// ValidateBackupPath validates a backup path for API use (no absolute paths allowed)
func ValidateBackupPath(customPath, workDir string) (string, error) {
	if customPath == "" {
		return filepath.Join(workDir, "backup"), nil
	}

	// API: reject absolute paths for security
	if filepath.IsAbs(customPath) {
		return "", fmt.Errorf("absolute paths are not allowed for backup directory for security reasons")
	}

	// Check for path traversal attempts
	if strings.Contains(customPath, "..") {
		return "", fmt.Errorf("path traversal not allowed in backup directory")
	}

	return filepath.Join(workDir, customPath), nil
}

// ValidateBackupPathCLI validates a backup path for CLI use (absolute paths allowed)
func ValidateBackupPathCLI(customPath, workDir string) string {
	if customPath == "" {
		return filepath.Join(workDir, "backup")
	}

	if filepath.IsAbs(customPath) {
		return filepath.Clean(customPath)
	}

	return filepath.Join(workDir, customPath)
}

// ResolveAuthDir returns the auth directory path, using the provided configAuthDir
// if set, otherwise defaulting to "auths" under the working directory.
// This centralizes the auth directory resolution logic used by both CLI and API.
func ResolveAuthDir(configAuthDir, workDir string) string {
	if configAuthDir != "" {
		return configAuthDir
	}
	return filepath.Join(workDir, "auths")
}
